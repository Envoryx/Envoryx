package sshd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/db"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/docker/dockertest"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/sshd"
	"github.com/seramos/staqio/internal/store"
)

type env struct {
	manager *project.Manager
	srv     *sshd.Server
	engine  *dockertest.Fake
	auth    *auth.Service
	st      *store.Store
	token   string
	projDir string
	cfgDir  string
	proj    project.View
	addr    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	engine := dockertest.New()
	cfgDir, projDir := t.TempDir(), t.TempDir()
	paths := func() (project.Paths, error) {
		return project.Paths{ConfigDir: cfgDir, ConfigHostDir: "/host/config", ProjectsDir: projDir, ProjectsHostDir: "/host/projects", PUID: os.Getuid(), PGID: os.Getgid()}, nil
	}
	sessions := auth.NewService(st, auth.Options{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, log)
	auditLog := audit.New(st.Audit, log)
	manager := project.NewManager(st, engine, runtime.Default(), paths, auditLog, project.Config{PortRangeStart: 20000, PortRangeEnd: 20010}, log)
	user, err := sessions.CreateInitialAdmin(context.Background(), "admin", "supersecret123")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.CreateAPIToken(context.Background(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role}, "phpstorm")
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.Create(context.Background(), project.CreateRequest{Name: "Shop", Docroot: "public", PHP: &project.PHPRequest{Version: "8.4"}, CreateStarter: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sshd.New(sshd.Deps{Auth: sessions, Store: st, Projects: manager, Engine: engine, Audit: auditLog, HostKeyPath: filepath.Join(cfgDir, "ssh", "host_ed25519"), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.ServeConn(ctx, c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return &env{manager: manager, srv: srv, engine: engine, auth: sessions, st: st, token: token, projDir: projDir, cfgDir: cfgDir, proj: view, addr: ln.Addr().String()}
}

func (e *env) dial(t *testing.T, user string, authMethod ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", e.addr, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{authMethod}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
}

func TestExecAndAuth(t *testing.T) {
	e := newEnv(t)
	var seen []string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		seen = append(seen, container+"|"+strings.Join(cmd, " ")+"|"+strings.Join(env, ","))
		if strings.Contains(cmd[2], "exit 3") {
			return "", 3, nil
		}
		return "PHP 8.4.0 (cli)\n" + string(stdin), 0, nil
	}

	if _, err := e.dial(t, "shop", ssh.Password("stq_wrong")); err == nil {
		t.Fatal("wrong token must be rejected")
	}
	if _, err := e.dial(t, "nope", ssh.Password(e.token)); err == nil {
		t.Fatal("unknown project must be rejected")
	}
	client, err := e.dial(t, "shop", ssh.Password(e.token))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	sess.Stdin = strings.NewReader("hello")
	out, err := sess.Output("php -v")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(string(out), "PHP 8.4.0") || !strings.HasSuffix(string(out), "hello") {
		t.Fatalf("output: %q", out)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "staqio-shop-php|/bin/sh -lc php -v|") || !strings.Contains(seen[0], "HOME=/home/staqio") {
		t.Fatalf("exec: %v", seen)
	}
	sess.Close()

	sess, _ = client.NewSession()
	err = sess.Run("exit 3")
	var exitErr *ssh.ExitError
	if !errorsAs(err, &exitErr) || exitErr.ExitStatus() != 3 {
		t.Fatalf("exit status: %v", err)
	}
	sess.Close()

	// PTY sessions use the terminal exec.
	sess, _ = client.NewSession()
	if err := sess.RequestPty("xterm", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdout, _ := sess.StdoutPipe()
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && e.engine.LastTerminal() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	term := e.engine.LastTerminal()
	if term == nil {
		t.Fatal("no terminal opened")
	}
	term.Finish("$ \r\n", 0)
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(stdout)
	_ = sess.Wait()
	if !strings.Contains(buf.String(), "$") {
		t.Fatalf("pty output: %q", buf.String())
	}
	recs := e.engine.Terminals()
	if got := strings.Join(recs[len(recs)-1].Opts.Cmd, " "); got != "/bin/sh -l" || recs[len(recs)-1].Opts.Cols != 120 {
		t.Fatalf("terminal opts: %+v", recs[len(recs)-1].Opts)
	}
}

func TestSFTPMapsProjectAndHome(t *testing.T) {
	e := newEnv(t)
	client, err := e.dial(t, "shop", ssh.Password(e.token))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sc, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	entries, err := sc.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	if strings.Join(names, ",") != "home,var" {
		t.Fatalf("root listing: %v", names)
	}
	if _, err := sc.ReadDir("/var/www/html/public"); err != nil {
		t.Fatalf("project dir: %v", err)
	}
	if err := sc.MkdirAll("/home/staqio/.phpstorm_helpers"); err != nil {
		t.Fatal(err)
	}
	f, err := sc.Create("/home/staqio/.phpstorm_helpers/phpinfo.php")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("<?php phpinfo();"))
	f.Close()
	if b, err := os.ReadFile(filepath.Join(e.cfgDir, "projects", e.proj.Project.ID, "home", ".phpstorm_helpers", "phpinfo.php")); err != nil || string(b) != "<?php phpinfo();" {
		t.Fatalf("home file on disk: %v %q", err, b)
	}
	if _, err := sc.Create("/var/www/html/public/new.php"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "public", "new.php")); err != nil {
		t.Fatalf("project file on disk: %v", err)
	}
	for _, bad := range []string{"/etc/passwd", "/var/www/html/../../etc/passwd", "/home/other"} {
		if _, err := sc.Open(bad); err == nil {
			t.Fatalf("%s must be inaccessible", bad)
		}
	}
	if _, err := sc.Stat("/var/www"); err != nil {
		t.Fatalf("virtual dir stat: %v", err)
	}
}

func TestPublicKeyAuth(t *testing.T) {
	e := newEnv(t)
	signer, err := ssh.NewSignerFromKey(mustKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dial(t, "shop", ssh.PublicKeys(signer)); err == nil {
		t.Fatal("unknown key must be rejected")
	}
	pub := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err := e.st.Settings.Set(context.Background(), sshd.SettingAuthorizedKeys, "# my laptop\n"+pub); err != nil {
		t.Fatal(err)
	}
	client, err := e.dial(t, "shop", ssh.PublicKeys(signer))
	if err != nil {
		t.Fatalf("key auth: %v", err)
	}
	client.Close()
	// Node service missing → user "shop.node" is rejected.
	if _, err := e.dial(t, "shop.node", ssh.PublicKeys(signer)); err == nil {
		t.Fatal("missing node service must reject")
	}
}

func TestPortForwardingRequiresGateway(t *testing.T) {
	e := newEnv(t)
	// Runtime image without socat → forwarding falls back to the project network.
	e.engine.ExecHandler = func(_ string, cmd []string, _ []string) (docker.ExecResult, error) {
		if len(cmd) == 3 && cmd[2] == "command -v socat" {
			return docker.ExecResult{ExitCode: 127}, nil
		}
		return docker.ExecResult{ExitCode: 0}, nil
	}
	// A local echo server stands in for the IDE backend inside the container.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = c.Write([]byte("backend:"))
				buf := make([]byte, 16)
				n, _ := c.Read(buf)
				_, _ = c.Write(buf[:n])
				c.Close()
			}()
		}
	}()
	var dialed string
	e.srv.SetDialForTest(func(ctx context.Context, addr string) (net.Conn, error) {
		dialed = addr
		return net.Dial("tcp", ln.Addr().String())
	})
	client, err := e.dial(t, "shop", ssh.Password(e.token))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Dial("tcp", "localhost:5990"); err == nil || !strings.Contains(err.Error(), "Gateway") {
		t.Fatalf("forwarding must be rejected while gateway is off: %v", err)
	}
	on := true
	if _, err := e.manager.Update(context.Background(), e.proj.Project.ID, project.UpdateRequest{IDEGateway: &on}); err != nil {
		t.Fatal(err)
	}
	// New target state is read per connection.
	client2, err := e.dial(t, "shop", ssh.Password(e.token))
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	conn, err := client2.Dial("tcp", "localhost:5990")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 32)
	n, _ := conn.Read(buf)
	if got := string(buf[:n]); !strings.HasPrefix(got, "backend:") {
		t.Fatalf("forwarded data: %q", got)
	}
	if dialed != "staqio-shop-php:5990" {
		t.Fatalf("dial target: %s", dialed)
	}
	if _, err := client2.Dial("tcp", "example.com:80"); err == nil {
		t.Fatal("foreign hosts must be rejected")
	}
}

// The IDE backend listens on 127.0.0.1 inside the container, so the tunnel is relayed by
// socat running in the container (docker exec) instead of over the project network.
func TestPortForwardingRelaysInsideContainer(t *testing.T) {
	e := newEnv(t)
	on := true
	if _, err := e.manager.Update(context.Background(), e.proj.Project.ID, project.UpdateRequest{IDEGateway: &on}); err != nil {
		t.Fatal(err)
	}
	var relayCmd []string
	e.engine.StreamHandler = func(container string, cmd []string, _ []string, stdin []byte) (string, int, error) {
		if container != "staqio-shop-php" {
			t.Errorf("relay container: %s", container)
		}
		relayCmd = cmd
		return "backend:" + string(stdin), 0, nil
	}
	e.srv.SetDialForTest(func(context.Context, string) (net.Conn, error) {
		t.Error("must not dial over the network when socat is available")
		return nil, errors.New("no")
	})
	client, err := e.dial(t, "shop", ssh.Password(e.token))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := client.Dial("tcp", "127.0.0.1:5990")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	_, _ = conn.Write([]byte("ping"))
	_ = conn.(interface{ CloseWrite() error }).CloseWrite()
	got, _ := io.ReadAll(conn)
	if string(got) != "backend:ping" {
		t.Fatalf("relayed data: %q", got)
	}
	if len(relayCmd) == 0 || relayCmd[0] != "socat" || relayCmd[len(relayCmd)-1] != "TCP:127.0.0.1:5990,connect-timeout=5" {
		t.Fatalf("relay command: %v", relayCmd)
	}
}
