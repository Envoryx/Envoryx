// Package sshd is Envoryx's embedded SSH server. It lets IDEs (PhpStorm/WebStorm remote
// interpreter, VS Code, plain ssh) run commands inside a project's application container
// and transfer files via SFTP – without exposing the Docker socket or a real shell on the
// host. The user name selects the project and container: "<slug>" is the application
// container (PHP, or Node when the project has no PHP), "<slug>.php" and "<slug>.node"
// select explicitly. The password is a Envoryx API token, or a public key from the
// settings is used.
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
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

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
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
	// dial connects forwarded ports over the project network (fallback for runtime images
	// without socat; overridden in tests).
	dial func(ctx context.Context, addr string) (net.Conn, error)
	// relayTool caches per container id whether socat is available for in-namespace relays.
	relayTool sync.Map
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
		// Clients offer every agent key before falling back to the password; each rejected
		// key is one try, so allow a full agent (OpenSSH's default is 6).
		MaxAuthTries:     10,
		PasswordCallback: s.passwordAuth,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return s.publicKeyAuth(conn, key)
		},
		ServerVersion: "SSH-2.0-Envoryx",
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
	block, err := ssh.MarshalPrivateKey(priv, "envoryx host key")
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
		s.fail(ip)
		s.d.Log.Info("ssh password rejected", "user", conn.User(), "remote", ip)
		return nil, errors.New("invalid token")
	}
	target, err := s.d.Projects.ResolveSSHUser(ctx, conn.User())
	if err != nil {
		s.fail(ip)
		s.d.Log.Info("ssh unknown project", "user", conn.User(), "remote", ip, "err", err)
		return nil, errors.New("unknown project")
	}
	// A shell in the container is "operate"; a token confined to other projects must not
	// even learn that this one exists.
	if err := p.Require(auth.ScopeOperate, target.Project.ID); err != nil {
		s.fail(ip)
		s.d.Log.Info("ssh token not permitted", "user", conn.User(), "token", p.TokenName, "remote", ip, "err", err)
		return nil, errors.New("token not permitted for this project")
	}
	s.limiter.reset(ip)
	return &ssh.Permissions{Extensions: map[string]string{"envoryx-user": p.Username, "envoryx-token": p.TokenName}}, nil
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
				s.fail(ip)
				s.d.Log.Info("ssh unknown project", "user", conn.User(), "remote", ip, "err", err)
				return nil, errors.New("unknown project")
			}
			s.limiter.reset(ip)
			return &ssh.Permissions{Extensions: map[string]string{"envoryx-user": "ssh-key", "envoryx-token": comment}}, nil
		}
	}
	// Not a failed attempt in the brute-force sense: clients routinely offer every key in
	// their agent before trying the password, and keys cannot be guessed.
	return nil, errors.New("unknown key")
}

// fail counts an authentication failure and logs when the address gets locked out.
func (s *Server) fail(ip string) {
	if s.limiter.fail(ip) {
		s.d.Log.Warn("ssh lockout: too many failed attempts", "remote", ip, "minutes", 5)
	}
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

// fail records a failure and reports whether it started a lockout.
func (l *failLimiter) fail(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.fails[ip]
	e.n++
	locked := e.n >= 10
	if locked {
		e.until = time.Now().Add(5 * time.Minute)
		e.n = 0
	}
	l.fails[ip] = e
	return locked
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
	go func() {
		// Global requests (remote forwarding, keepalives) are not supported; log them so a
		// client that depends on one can be diagnosed.
		for req := range reqs {
			if req.Type != "keepalive@openssh.com" {
				s.d.Log.Info("ssh global request not supported", "user", sc.User(), "type", req.Type)
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()

	target, err := s.d.Projects.ResolveSSHUser(ctx, sc.User())
	if err != nil {
		s.d.Log.Warn("ssh user resolution failed after auth", "user", sc.User(), "err", err)
		return
	}
	actx := audit.WithClientIP(auth.WithPrincipal(ctx, auth.Principal{Username: sc.Permissions.Extensions["envoryx-user"], TokenName: sc.Permissions.Extensions["envoryx-token"]}), remoteIP(nc.RemoteAddr()))
	s.d.Audit.Log(actx, "ssh.login", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "service": string(target.Kind)})

	for ch := range chans {
		if ch.ChannelType() == "direct-tcpip" {
			go s.handleForward(actx, ch, target)
			continue
		}
		if ch.ChannelType() != "session" {
			s.d.Log.Warn("ssh channel type not supported", "user", sc.User(), "type", ch.ChannelType())
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
			s.d.Log.Debug("ssh session request not supported", "type", req.Type)
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
		fmt.Fprintf(ch.Stderr(), "Envoryx: project %s is not running – start it in the Envoryx UI first.\r\n", target.Project.Name)
		return 1
	}
	env := append(append([]string{}, target.Env...), st.env...)
	s.d.Audit.Log(ctx, "ssh.exec", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "type": kind, "pty": st.pty, "command": truncate(strings.Join(cmd[2:], " "), 200)})
	s.d.Log.Info("ssh exec", "project", target.Project.Slug, "type", kind, "pty", st.pty, "command", truncate(strings.Join(cmd[2:], " "), 400))
	// At debug level the first bytes of output and the exit code are logged, which is
	// what IDE clients hide when a remote command fails.
	var out io.Writer = ch
	var tap *outputTap
	if kind == "exec" && s.d.Log.Enabled(ctx, slog.LevelDebug) {
		tap = &outputTap{limit: 1024}
		out = io.MultiWriter(ch, tap)
		defer func() {
			s.d.Log.Debug("ssh exec done", "project", target.Project.Slug, "command", truncate(strings.Join(cmd[2:], " "), 120), "exit", tap.code, "output", tap.String())
		}()
	}
	if st.pty {
		term, err := s.d.Engine.OpenTerminal(ctx, target.ContainerID, docker.TerminalOptions{Cmd: cmd, Env: env, User: target.User, WorkingDir: target.WorkingDir, Cols: st.cols, Rows: st.rows})
		if err != nil {
			fmt.Fprintf(ch.Stderr(), "Envoryx: %v\r\n", err)
			return 1
		}
		mu.Lock()
		st.term = term
		mu.Unlock()
		defer term.Close()
		go func() { _, _ = io.Copy(term.Input(), ch) }()
		_, _ = io.Copy(out, term.Output())
		code, err := term.ExitCode(ctx)
		if err != nil {
			return 1
		}
		tap.setCode(code)
		return code
	}
	code, err := s.d.Engine.ExecStream(ctx, target.ContainerID, docker.ExecStreamOptions{Cmd: cmd, Env: env, User: target.User, WorkingDir: target.WorkingDir, Stdin: ch, Stdout: out, Stderr: ch.Stderr()})
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "Envoryx: %v\r\n", err)
		return 1
	}
	tap.setCode(code)
	return code
}

// outputTap keeps the first bytes of a command's output for debug logging.
type outputTap struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	code  int
}

func (t *outputTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	if room := t.limit - len(t.buf); room > 0 {
		t.buf = append(t.buf, p[:min(room, len(p))]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *outputTap) setCode(code int) {
	if t != nil {
		t.mu.Lock()
		t.code = code
		t.mu.Unlock()
	}
}

func (t *outputTap) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.buf), "?")
}

// countingReader / countingWriter measure tunnel traffic for the forward log.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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
// the target container, which Envoryx reaches over the project network.
func (s *Server) handleForward(ctx context.Context, ch ssh.NewChannel, target project.ExecTarget) {
	host, port, ok := parseForward(ch.ExtraData())
	if !ok {
		_ = ch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	reject := func(reason ssh.RejectionReason, msg string) {
		s.d.Log.Warn("ssh forward rejected", "project", target.Project.Slug, "target", net.JoinHostPort(host, strconv.Itoa(int(port))), "reason", msg)
		_ = ch.Reject(reason, msg)
	}
	if !target.Gateway {
		reject(ssh.Prohibited, "port forwarding is disabled for this project (enable JetBrains Gateway in the IDE tab)")
		return
	}
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
	default:
		reject(ssh.Prohibited, "only localhost ports of the project container can be forwarded")
		return
	}
	if !target.Running {
		reject(ssh.ConnectionFailed, "project is not running")
		return
	}
	// The IDE backend binds to 127.0.0.1 inside the container, so the relay has to run in
	// the container's network namespace. Older runtime images without socat fall back to
	// dialing the container over the project network (reaches 0.0.0.0 listeners only).
	if s.hasSocat(ctx, target) {
		addr, err := s.listenAddress(ctx, target, int(port))
		if err != nil {
			reject(ssh.ConnectionFailed, err.Error())
			return
		}
		channel, reqs, err := ch.Accept()
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		s.d.Log.Info("ssh forward", "project", target.Project.Slug, "port", port, "via", "socat", "listener", addr)
		s.d.Audit.Log(ctx, "ssh.forward", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "port": port})
		s.relayExec(ctx, target, addr, channel)
		return
	}
	conn, err := s.dial(ctx, net.JoinHostPort(target.ContainerName, strconv.Itoa(int(port))))
	if err != nil {
		reject(ssh.ConnectionFailed, "connect: "+err.Error()+" (update the runtime image for localhost forwarding)")
		return
	}
	s.d.Log.Info("ssh forward", "project", target.Project.Slug, "port", port, "via", "network")
	channel, reqs, err := ch.Accept()
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	s.d.Audit.Log(ctx, "ssh.forward", "project", target.Project.ID, map[string]any{"name": target.Project.Name, "port": port, "via": "network"})
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

// hasSocat reports whether the target container ships socat (cached per container id, so
// a recreated container is probed again).
func (s *Server) hasSocat(ctx context.Context, target project.ExecTarget) bool {
	if v, ok := s.relayTool.Load(target.ContainerID); ok {
		return v.(bool)
	}
	res, err := s.d.Engine.Exec(ctx, target.ContainerID, []string{"sh", "-c", "command -v socat"}, nil)
	has := err == nil && res.ExitCode == 0
	s.relayTool.Store(target.ContainerID, has)
	return has
}

// listenAddress finds where port is bound inside the container by reading its
// /proc/net/tcp{,6} (a docker exec sees the container's own network namespace). The IDE
// backend binds 127.0.0.1, Gateway's host worker may bind the container's address; a
// port nobody listens on is reported instead of silently accepting the channel.
func (s *Server) listenAddress(ctx context.Context, target project.ExecTarget, port int) (string, error) {
	res, err := s.d.Engine.Exec(ctx, target.ContainerID, []string{"sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"}, nil)
	if err != nil {
		return "", fmt.Errorf("inspect listeners: %w", err)
	}
	addr := parseListenAddress(res.Stdout, port)
	if addr == "" {
		return "", fmt.Errorf("nothing is listening on port %d inside %s", port, target.ContainerName)
	}
	return addr, nil
}

// parseListenAddress returns the socat address of a listening (state 0A) TCP socket on
// port from /proc/net/tcp{,6} content, or "" when there is none. Wildcard and loopback
// listeners map to the loopback address of their family.
func parseListenAddress(procNet string, port int) string {
	best := ""
	for _, line := range strings.Split(procNet, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		hexAddr, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		raw, err := hex.DecodeString(hexAddr)
		if err != nil {
			continue
		}
		switch len(raw) {
		case 4:
			// Little-endian word: 0100007F is 127.0.0.1.
			ip := net.IPv4(raw[3], raw[2], raw[1], raw[0])
			if ip.IsUnspecified() || ip.IsLoopback() {
				return "TCP4:127.0.0.1:" + strconv.Itoa(port)
			}
			best = "TCP4:" + ip.String() + ":" + strconv.Itoa(port)
		case 16:
			// Four little-endian 32-bit words.
			ip := make(net.IP, 16)
			for w := 0; w < 4; w++ {
				for b := 0; b < 4; b++ {
					ip[w*4+b] = raw[w*4+3-b]
				}
			}
			if v4 := ip.To4(); v4 != nil {
				// IPv4-mapped (::ffff:127.0.0.1): a dual-stack socket bound to an IPv4
				// address, which is how the JetBrains backend binds 127.0.0.1. Reach it
				// over IPv4; [::1] would be refused.
				if v4.IsUnspecified() || v4.IsLoopback() {
					return "TCP4:127.0.0.1:" + strconv.Itoa(port)
				}
				best = "TCP4:" + v4.String() + ":" + strconv.Itoa(port)
				continue
			}
			if ip.IsUnspecified() || ip.IsLoopback() {
				if best == "" {
					// A dual-stack wildcard also answers on 127.0.0.1; keep looking for an
					// explicit IPv4 listener first.
					best = "TCP6:[::1]:" + strconv.Itoa(port)
					if ip.IsUnspecified() {
						best = "TCP4:127.0.0.1:" + strconv.Itoa(port)
					}
				}
				continue
			}
			if best == "" {
				best = "TCP6:[" + ip.String() + "]:" + strconv.Itoa(port)
			}
		}
	}
	return best
}

// relayExec pipes the channel through `socat` running inside the container, which
// connects to addr in the container's own network namespace. socat half-closes the
// socket on stdin EOF and exits once the backend closes (or after 5 s of silence in the
// remaining direction).
func (s *Server) relayExec(ctx context.Context, target project.ExecTarget, addr string, channel ssh.Channel) {
	defer channel.Close()
	var stderr strings.Builder
	in, out := &countingReader{r: channel}, &countingWriter{w: channel}
	started := time.Now()
	code, err := s.d.Engine.ExecStream(ctx, target.ContainerID, docker.ExecStreamOptions{
		Cmd:    []string{"socat", "-t", "5", "STDIO", addr + ",connect-timeout=5"},
		User:   target.User,
		Stdin:  in,
		Stdout: out,
		Stderr: &stderr,
	})
	_ = channel.CloseWrite()
	if err != nil || code != 0 {
		s.d.Log.Warn("ssh forward failed", "project", target.Project.Slug, "container", target.ContainerName, "addr", addr, "exit", code, "err", err, "stderr", strings.TrimSpace(stderr.String()), "in", in.n, "out", out.n, "after", time.Since(started).Round(time.Millisecond))
		return
	}
	s.d.Log.Info("ssh forward closed", "project", target.Project.Slug, "addr", addr, "in", in.n, "out", out.n, "after", time.Since(started).Round(time.Millisecond))
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
