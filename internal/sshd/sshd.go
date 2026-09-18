// Package sshd is Staqio's embedded SSH server. It lets IDEs (PhpStorm remote interpreter,
// VS Code, plain ssh) run commands inside a project's application container and
// transfer files via SFTP – without exposing the Docker socket or a real shell on the
// host. The user name selects the project ("<slug>" = PHP, "<slug>.node" = Node), the
// password is a Staqio API token, or a public key from the settings is used.
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/store"
)

// SettingAuthorizedKeys holds the operator's SSH public keys (authorized_keys format).
const SettingAuthorizedKeys = "ssh_authorized_keys"

// Deps are the collaborators of the server.
type Deps struct {
	Auth     *auth.Service
	Store    *store.Store
	Projects *project.Manager
	Engine   docker.Engine
	Audit    *audit.Logger
	// HostKeyPath is where the Ed25519 host key lives (created on first start).
	HostKeyPath string
	Log         *slog.Logger
}

// Server accepts SSH connections.
type Server struct {
	d       Deps
	config  *ssh.ServerConfig
	signer  ssh.Signer
	limiter *failLimiter
	// dial connects forwarded ports (overridden in tests).
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

// New loads or creates the host key and prepares the server configuration.
func New(d Deps) (*Server, error) {
	signer, err := loadOrCreateHostKey(d.HostKeyPath)
	if err != nil {
		return nil, err
	}
	s := &Server{d: d, signer: signer, limiter: newFailLimiter()}
	s.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	s.config = &ssh.ServerConfig{
		MaxAuthTries:     4,
		PasswordCallback: s.passwordAuth,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return s.publicKeyAuth(conn, key)
		},
		ServerVersion: "SSH-2.0-Staqio",
	}
	s.config.AddHostKey(signer)
	return s, nil
}

// Fingerprint returns the host key fingerprint for the UI.
func (s *Server) Fingerprint() string { return ssh.FingerprintSHA256(s.signer.PublicKey()) }

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(raw)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "staqio host key")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

// ---- Authentication ------------------------------------------------------------------

func (s *Server) passwordAuth(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	ip := remoteIP(conn.RemoteAddr())
	if !s.limiter.allow(ip) {
		return nil, errors.New("too many failed attempts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := s.d.Auth.ValidateAPIToken(ctx, string(password))
	if err != nil {
		s.limiter.fail(ip)
		return nil, errors.New("invalid token")
	}
	if _, err := s.d.Projects.ResolveSSHUser(ctx, conn.User()); err != nil {
		s.limiter.fail(ip)
		return nil, errors.New("unknown project")
	}
	s.limiter.reset(ip)
	return &ssh.Permissions{Extensions: map[string]string{"staqio-user": p.Username, "staqio-token": p.TokenName}}, nil
}

func (s *Server) publicKeyAuth(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ip := remoteIP(conn.RemoteAddr())
	if !s.limiter.allow(ip) {
		return nil, errors.New("too many failed attempts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys, _ := s.d.Store.Settings.Get(ctx, SettingAuthorizedKeys)
	want := key.Marshal()
	for _, line := range strings.Split(keys, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}
		if string(pk.Marshal()) == string(want) {
			if _, err := s.d.Projects.ResolveSSHUser(ctx, conn.User()); err != nil {
				s.limiter.fail(ip)
				return nil, errors.New("unknown project")
			}
			s.limiter.reset(ip)
			return &ssh.Permissions{Extensions: map[string]string{"staqio-user": "ssh-key", "staqio-token": comment}}, nil
		}
	}
	s.limiter.fail(ip)
	return nil, errors.New("unknown key")
}

func remoteIP(a net.Addr) string {
	if h, _, err := net.SplitHostPort(a.String()); err == nil {
		return h
	}
	return a.String()
}

// failLimiter delays repeated failures per IP (10 failures → 5 minutes lockout).
type failLimiter struct {
	mu    sync.Mutex
	fails map[string]struct {
		n     int
		until time.Time
	}
}

func newFailLimiter() *failLimiter {
	return &failLimiter{fails: map[string]struct {
		n     int
		until time.Time
	}{}}
}

func (l *failLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.fails[ip]
	return e.until.IsZero() || time.Now().After(e.until)
}

func (l *failLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.fails[ip]
	e.n++
	if e.n >= 10 {
		e.until = time.Now().Add(5 * time.Minute)
		e.n = 0
	}
	l.fails[ip] = e
}

func (l *failLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

// ---- Serving -------------------------------------------------------------------------

// Run listens on addr until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh listen %s: %w", addr, err)
	}
	s.d.Log.Info("ssh listening", "addr", addr, "fingerprint", s.Fingerprint())
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.d.Log.Warn("ssh accept", "err", err)
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

// ServeConn handles one already accepted connection (exported for tests).
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) { s.handleConn(ctx, conn) }

func (s *Server) handleConn(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.config)
	if err != nil {
		s.d.Log.Debug("ssh handshake failed", "remote", nc.RemoteAddr(), "err", err)
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sc.Close()
	go ssh.DiscardRequests(reqs)

	target, err := s.d.Projects.ResolveSSHUser(ctx, sc.User())
	if err != nil {
		return
	}
	actx := audit.WithClientIP(auth.WithPrincipal(ctx, auth.Principal{Username: sc.Permissions.Extensions["staqio-user"], TokenName: sc.Permissions.Extensions["staqio-token"]}), remoteIP(nc.RemoteAddr()))
	s.d.Audit.Log(actx, "ssh.login", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "service": string(target.Kind)})

	for ch := range chans {
		if ch.ChannelType() == "direct-tcpip" {
			go s.handleForward(actx, ch, target)
			continue
		}
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "only sessions and port forwarding are supported")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(actx, channel, requests, target)
	}
}

type session struct {
	pty        bool
	cols, rows uint
	env        []string
	term       docker.Terminal
}

func (s *Server) handleSession(ctx context.Context, ch ssh.Channel, reqs <-chan *ssh.Request, target project.ExecTarget) {
	defer ch.Close()
	st := &session{cols: 120, rows: 40}
	var mu sync.Mutex
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			term, cols, rows := parsePty(req.Payload)
			st.pty, st.cols, st.rows = true, cols, rows
			if term != "" {
				st.env = append(st.env, "TERM="+term)
			}
			_ = req.Reply(true, nil)
		case "window-change":
			cols, rows := parseWindow(req.Payload)
			mu.Lock()
			st.cols, st.rows = cols, rows
			if st.term != nil {
				_ = st.term.Resize(ctx, cols, rows)
			}
			mu.Unlock()
			_ = req.Reply(true, nil)
		case "env":
			if k, v, ok := parseEnv(req.Payload); ok && allowedEnv(k) {
				st.env = append(st.env, k+"="+v)
			}
			_ = req.Reply(true, nil)
		case "shell", "exec":
			var cmd []string
			if req.Type == "exec" {
				line := parseString(req.Payload)
				if strings.TrimSpace(line) == "" {
					_ = req.Reply(false, nil)
					continue
				}
				cmd = []string{"/bin/sh", "-lc", line}
			} else {
				cmd = []string{"/bin/sh", "-l"}
			}
			_ = req.Reply(true, nil)
			code := s.run(ctx, ch, st, &mu, target, cmd, req.Type)
			sendExit(ch, code)
			return
		case "subsystem":
			if parseString(req.Payload) != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			s.serveSFTP(ctx, ch, target)
			sendExit(ch, 0)
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func allowedEnv(k string) bool {
	switch k {
	case "LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "XDEBUG_TRIGGER", "XDEBUG_SESSION", "XDEBUG_CONFIG", "PHP_IDE_CONFIG", "APP_ENV", "CI":
		return true
	}
	return false
}

// run executes a command in the target container, wiring the SSH channel to it.
func (s *Server) run(ctx context.Context, ch ssh.Channel, st *session, mu *sync.Mutex, target project.ExecTarget, cmd []string, kind string) int {
	if target.ContainerID == "" || !target.Running {
		fmt.Fprintf(ch.Stderr(), "Staqio: project %s is not running – start it in the Staqio UI first.\r\n", target.Project.Name)
		return 1
	}
	env := append(append([]string{}, target.Env...), st.env...)
	s.d.Audit.Log(ctx, "ssh.exec", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "type": kind, "pty": st.pty, "command": truncate(strings.Join(cmd[2:], " "), 200)})
	if st.pty {
		term, err := s.d.Engine.OpenTerminal(ctx, target.ContainerID, docker.TerminalOptions{Cmd: cmd, Env: env, User: target.User, WorkingDir: target.WorkingDir, Cols: st.cols, Rows: st.rows})
		if err != nil {
			fmt.Fprintf(ch.Stderr(), "Staqio: %v\r\n", err)
			return 1
		}
		mu.Lock()
		st.term = term
		mu.Unlock()
		defer term.Close()
		go func() { _, _ = io.Copy(term.Input(), ch) }()
		_, _ = io.Copy(ch, term.Output())
		code, err := term.ExitCode(ctx)
		if err != nil {
			return 1
		}
		return code
	}
	code, err := s.d.Engine.ExecStream(ctx, target.ContainerID, docker.ExecStreamOptions{Cmd: cmd, Env: env, User: target.User, WorkingDir: target.WorkingDir, Stdin: ch, Stdout: ch, Stderr: ch.Stderr()})
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "Staqio: %v\r\n", err)
		return 1
	}
	return code
}

func (s *Server) serveSFTP(ctx context.Context, ch ssh.Channel, target project.ExecTarget) {
	s.d.Audit.Log(ctx, "ssh.sftp", "project", target.Project.ID, map[string]any{"name": target.Project.Name})
	fs := newProjectFS(target)
	srv := sftp.NewRequestServer(ch, sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs})
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		s.d.Log.Debug("sftp session ended", "err", err)
	}
	_ = srv.Close()
}

func sendExit(ch ssh.Channel, code int) {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(code))
	_, _ = ch.SendRequest("exit-status", false, buf)
}

// handleForward serves a direct-tcpip channel (ssh -L / IDE tunnels). Forwarding is only
// allowed for projects with JetBrains Gateway enabled and only to "localhost" ports of
// the target container, which Staqio reaches over the project network.
func (s *Server) handleForward(ctx context.Context, ch ssh.NewChannel, target project.ExecTarget) {
	host, port, ok := parseForward(ch.ExtraData())
	if !ok {
		_ = ch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	if !target.Gateway {
		_ = ch.Reject(ssh.Prohibited, "port forwarding is disabled for this project (enable JetBrains Gateway in the IDE tab)")
		return
	}
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
	default:
		_ = ch.Reject(ssh.Prohibited, "only localhost ports of the project container can be forwarded")
		return
	}
	if !target.Running {
		_ = ch.Reject(ssh.ConnectionFailed, "project is not running")
		return
	}
	conn, err := s.dial(ctx, net.JoinHostPort(target.ContainerName, strconv.Itoa(int(port))))
	if err != nil {
		_ = ch.Reject(ssh.ConnectionFailed, "connect: "+err.Error())
		return
	}
	channel, reqs, err := ch.Accept()
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	s.d.Audit.Log(ctx, "ssh.forward", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "port": port})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, channel)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() { _, _ = io.Copy(channel, conn); _ = channel.CloseWrite(); done <- struct{}{} }()
	<-done
	<-done
	_ = channel.Close()
	_ = conn.Close()
}

func parseForward(b []byte) (host string, port uint32, ok bool) {
	host = parseString(b)
	off := 4 + len(host)
	if len(b) < off+4 {
		return "", 0, false
	}
	port = binary.BigEndian.Uint32(b[off:])
	if port == 0 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

// ---- Payload parsing --------------------------------------------------------------------

func parseString(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(b)
	if int(n) > len(b)-4 {
		return ""
	}
	return string(b[4 : 4+n])
}

func parsePty(b []byte) (term string, cols, rows uint) {
	term = parseString(b)
	off := 4 + len(term)
	if len(b) < off+8 {
		return term, 120, 40
	}
	cols = uint(binary.BigEndian.Uint32(b[off:]))
	rows = uint(binary.BigEndian.Uint32(b[off+4:]))
	return term, clampDim(cols, 120), clampDim(rows, 40)
}

func parseWindow(b []byte) (cols, rows uint) {
	if len(b) < 8 {
		return 120, 40
	}
	return clampDim(uint(binary.BigEndian.Uint32(b)), 120), clampDim(uint(binary.BigEndian.Uint32(b[4:])), 40)
}

func clampDim(v, def uint) uint {
	if v == 0 || v > 1000 {
		return def
	}
	return v
}

func parseEnv(b []byte) (string, string, bool) {
	k := parseString(b)
	if k == "" || len(b) < 4+len(k) {
		return "", "", false
	}
	v := parseString(b[4+len(k):])
	return k, v, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
