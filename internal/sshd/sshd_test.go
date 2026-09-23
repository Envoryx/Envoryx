package sshd_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/sshd"
	"github.com/envoryx/envoryx/internal/store"
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
	token, _, err := sessions.CreateAPIToken(context.Background(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role}, auth.TokenSpec{Name: "phpstorm", Scope: auth.ScopeOperate})
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

func (e *env) dial(t *testing.T, user string, authMethods ...ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", e.addr, &ssh.ClientConfig{User: user, Auth: authMethods, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
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
	// Scope and project restriction apply to SSH as well.
	u, _ := e.st.Users.ByUsername(context.Background(), "admin")
	admin := auth.Principal{UserID: u.ID, Username: u.Username, Role: u.Role}
	readToken, _, err := e.auth.CreateAPIToken(context.Background(), admin, auth.TokenSpec{Name: "monitor", Scope: auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dial(t, "shop", ssh.Password(readToken)); err == nil {
		t.Fatal("a read token must not open a shell")
	}
	otherToken, _, err := e.auth.CreateAPIToken(context.Background(), admin, auth.TokenSpec{Name: "other", Scope: auth.ScopeAdmin, Projects: []string{"00000000-0000-0000-0000-000000000000"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dial(t, "shop", ssh.Password(otherToken)); err == nil {
		t.Fatal("a token confined to another project must be rejected")
	}
	confinedToken, _, err := e.auth.CreateAPIToken(context.Background(), admin, auth.TokenSpec{Name: "shop", Scope: auth.ScopeOperate, Projects: []string{e.proj.Project.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := e.dial(t, "shop", ssh.Password(confinedToken)); err != nil {
		t.Fatalf("a token confined to this project must log in: %v", err)
	} else {
		c.Close()
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
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "envoryx-shop-php|/bin/sh -lc php -v|") || !strings.Contains(seen[0], "HOME=/home/envoryx") || !strings.Contains(seen[0], "SHELL=/bin/sh") {
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
	if err := sc.MkdirAll("/home/envoryx/.phpstorm_helpers"); err != nil {
		t.Fatal(err)
	}
	f, err := sc.Create("/home/envoryx/.phpstorm_helpers/phpinfo.php")
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
	// Paths outside the mounts – traversal included – are the container's business, never
	// files of the Envoryx host: here the container refuses them.
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		return "", 1, nil
	}
	for _, bad := range []string{"/etc/passwd", "/var/www/html/../../etc/passwd", "/home/other"} {
		if _, err := sc.Open(bad); err == nil {
			t.Fatalf("%s must be refused when the container refuses it", bad)
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
	if dialed != "envoryx-shop-php:5990" {
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
	// socat present; /proc/net/tcp shows the backend bound to 127.0.0.1:5990 (0x1766).
	e.engine.ExecHandler = func(_ string, cmd []string, _ []string) (docker.ExecResult, error) {
		if strings.Contains(cmd[2], "/proc/net/tcp") {
			return docker.ExecResult{Stdout: "  sl  local_address rem_address   st\n   0: 0100007F:1766 00000000:0000 0A 00000000:00000000 00:00000000 00000000    99        0 1 0 0\n"}, nil
		}
		return docker.ExecResult{ExitCode: 0}, nil
	}
	var relayCmd []string
	e.engine.StreamHandler = func(container string, cmd []string, _ []string, stdin []byte) (string, int, error) {
		if container != "envoryx-shop-php" {
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
	if len(relayCmd) == 0 || relayCmd[0] != "socat" || relayCmd[len(relayCmd)-1] != "TCP4:127.0.0.1:5990,connect-timeout=5" {
		t.Fatalf("relay command: %v", relayCmd)
	}
	// A port nobody listens on is rejected with a reason instead of a dead tunnel.
	if _, err := client.Dial("tcp", "127.0.0.1:5991"); err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Fatalf("unbound port: %v", err)
	}
}

// IDE clients offer every agent key before the password; those probes must neither hit
// MaxAuthTries nor lock the address out.
func TestAgentKeyProbesDoNotLockOut(t *testing.T) {
	e := newEnv(t)
	var signers []ssh.Signer
	for range 7 {
		s, err := ssh.NewSignerFromKey(mustKey(t))
		if err != nil {
			t.Fatal(err)
		}
		signers = append(signers, s)
	}
	for range 3 {
		client, err := e.dial(t, "shop", ssh.PublicKeys(signers...), ssh.Password(e.token))
		if err != nil {
			t.Fatalf("keys then password: %v", err)
		}
		client.Close()
	}
}

// With Gateway enabled the shared IDE cache (/config/jetbrains, bind-mounted at
// ~/.cache/JetBrains) is part of the SFTP tree, exactly where the container sees it.
func TestSFTPShowsJetBrainsCacheWithGateway(t *testing.T) {
	e := newEnv(t)
	on := true
	if _, err := e.manager.Update(context.Background(), e.proj.Project.ID, project.UpdateRequest{IDEGateway: &on}); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(e.cfgDir, "jetbrains", "RemoteDev", "dist", "abc_PhpStorm")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "product-info.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	// ~/.cache does not exist in the project home yet; the mount point is still listed.
	entries, err := sc.ReadDir("/home/envoryx/.cache")
	if err != nil {
		t.Fatalf("list .cache: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "JetBrains" || !entries[0].IsDir() {
		t.Fatalf(".cache listing: %v", entries)
	}
	if st, err := sc.Stat("/home/envoryx/.cache/JetBrains/RemoteDev/dist/abc_PhpStorm/product-info.json"); err != nil || st.Size() != 2 {
		t.Fatalf("dist file: %v %v", err, st)
	}
	f, err := sc.Create("/home/envoryx/.cache/JetBrains/RemoteDev/dist/abc_PhpStorm/uploaded")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Stat(filepath.Join(dist, "uploaded")); err != nil {
		t.Fatalf("upload landed elsewhere: %v", err)
	}
}

// IDE clients keep stdin open until they receive the exit status; a command that never
// reads stdin (`dd if=file`) must still complete.
func TestExecCompletesWithOpenStdin(t *testing.T) {
	e := newEnv(t)
	e.engine.ReadsStdin = func(cmd []string) bool { return !strings.Contains(cmd[2], "dd if=") }
	e.engine.StreamHandler = func(_ string, cmd []string, _ []string, _ []byte) (string, int, error) {
		return "content", 0, nil
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
	defer sess.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	sess.Stdin = pr
	done := make(chan error, 1)
	var out bytes.Buffer
	sess.Stdout = &out
	go func() { done <- sess.Run("dd if=/etc/xdg/JetBrains/RemoteDev/disableManualDeployment") }()
	select {
	case err := <-done:
		if err != nil || out.String() != "content" {
			t.Fatalf("run: %v %q", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exec did not complete while stdin stayed open")
	}
}

// JetBrains IDEs ask to trust the host with an MD5 fingerprint; the UI has to offer the
// same key in that form next to the SHA256 one.
func TestFingerprintsNameTheServedKey(t *testing.T) {
	e := newEnv(t)
	var hostKey ssh.PublicKey
	c, err := ssh.Dial("tcp", e.addr, &ssh.ClientConfig{
		User: "shop", Auth: []ssh.AuthMethod{ssh.Password(e.token)}, Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { hostKey = key; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if got, want := e.srv.Fingerprint(), ssh.FingerprintSHA256(hostKey); got != want {
		t.Errorf("Fingerprint() = %s, want %s", got, want)
	}
	if got, want := e.srv.FingerprintMD5(), ssh.FingerprintLegacyMD5(hostKey); got != want {
		t.Errorf("FingerprintMD5() = %s, want %s", got, want)
	}
}

// Outside the bind mounts SFTP behaves like a server on the container: PhpStorm checks that
// /usr/local/bin/php exists, PyCharm uploads the project to /tmp/pycharm_project_* before
// its settings can be changed, and both put their helpers under ~/.
func TestSFTPServesTheContainerOutsideTheMounts(t *testing.T) {
	e := newEnv(t)
	files := map[string]string{} // the container's files outside the mounts
	var cmds []string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		if container != "envoryx-shop-php" {
			t.Errorf("command in %s", container)
		}
		arg := cmd[len(cmd)-1]
		switch {
		case cmd[0] == "stat":
			cmds = append(cmds, strings.Join(cmd[:len(cmd)-1], " "))
			if arg == "/usr/local/bin/php" {
				return "81ed 21784656 1789777866\n", 0, nil
			}
			if _, ok := files[arg]; ok {
				return fmt.Sprintf("81a4 %d 1789777866\n", len(files[arg])), 0, nil
			}
			return "", 1, nil
		case cmd[0] == "mkdir", cmd[0] == "rm", cmd[0] == "rmdir", cmd[0] == "chmod":
			cmds = append(cmds, strings.Join(cmd, " "))
			if strings.HasPrefix(arg, "/root/") || strings.HasPrefix(arg, "/home/someone/") {
				return "", 1, nil
			}
			if cmd[0] == "rm" {
				delete(files, arg)
			}
			return "", 0, nil
		case cmd[0] == "mv":
			files[cmd[3]] = files[cmd[2]]
			delete(files, cmd[2])
			return "", 0, nil
		case cmd[0] == "sh" && cmd[3] == "envoryx-write":
			if strings.HasPrefix(cmd[4], "/usr/") {
				return "", 1, nil
			}
			files[cmd[4]] = string(stdin)
			return "", 0, nil
		case cmd[0] == "sh" && cmd[3] == "envoryx-read":
			content, ok := files[cmd[4]]
			if !ok {
				return "", 1, nil
			}
			return content, 0, nil
		case cmd[0] == "sh" && cmd[3] == "envoryx-ls":
			var out strings.Builder
			for p, content := range files {
				if path.Dir(p) == cmd[4] {
					fmt.Fprintf(&out, "81a4 %d 1789777866/%s\n", len(content), path.Base(p))
				}
			}
			return out.String(), 0, nil
		}
		return "", 0, nil
	}
	e.engine.StreamStderr = func(container string, cmd []string) string {
		switch arg := cmd[len(cmd)-1]; {
		case cmd[0] == "mkdir" && strings.HasPrefix(arg, "/home/someone/"):
			return "mkdir: cannot create directory: No such file or directory"
		case cmd[0] == "sh" && cmd[3] == "envoryx-read":
			return "head: " + cmd[4] + ": No such file or directory"
		case cmd[0] == "sh" && cmd[3] == "envoryx-write":
			return "sh: can't create " + cmd[4] + ": Permission denied"
		}
		return ""
	}
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

	for _, stat := range []func(string) (os.FileInfo, error){sc.Lstat, sc.Stat} {
		info, err := stat("/usr/local/bin/php")
		if err != nil {
			t.Fatalf("interpreter stat: %v", err)
		}
		if info.Size() != 21784656 || info.Mode() != 0o755 || info.ModTime().Unix() != 1789777866 {
			t.Fatalf("interpreter stat: size %d mode %v mtime %v", info.Size(), info.Mode(), info.ModTime())
		}
	}
	if cmds[0] != "stat -c %f %s %Y %u %g --" || cmds[1] != "stat -L -c %f %s %Y %u %g --" {
		t.Fatalf("stat commands: %q", cmds)
	}
	if _, err := sc.Stat("/usr/local/bin/nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file outside the mounts: %v", err)
	}

	// PyCharm's first step: its default sync folder and the project in it.
	if err := sc.Mkdir("/tmp/pycharm_project_1234"); err != nil {
		t.Fatalf("mkdir outside the mounts: %v", err)
	}
	w, err := sc.Create("/tmp/pycharm_project_1234/hello.py")
	if err != nil {
		t.Fatalf("create outside the mounts: %v", err)
	}
	if _, err := w.Write([]byte("print('hi')\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("upload outside the mounts: %v", err)
	}
	if files["/tmp/pycharm_project_1234/hello.py"] != "print('hi')\n" {
		t.Fatalf("uploaded content: %q", files["/tmp/pycharm_project_1234/hello.py"])
	}
	if err := sc.Chmod("/tmp/pycharm_project_1234/hello.py", 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := sc.Open("/tmp/pycharm_project_1234/hello.py")
	if err != nil {
		t.Fatalf("open outside the mounts: %v", err)
	}
	if b, _ := io.ReadAll(r); string(b) != "print('hi')\n" {
		t.Fatalf("read back: %q", b)
	}
	r.Close()
	entries, err := sc.ReadDir("/tmp/pycharm_project_1234")
	if err != nil || len(entries) != 1 || entries[0].Name() != "hello.py" || entries[0].Size() != 12 {
		t.Fatalf("listing outside the mounts: %v %v", entries, err)
	}
	if err := sc.Rename("/tmp/pycharm_project_1234/hello.py", "/tmp/pycharm_project_1234/main.py"); err != nil {
		t.Fatal(err)
	}
	if err := sc.Remove("/tmp/pycharm_project_1234/main.py"); err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("rename/remove left %v", files)
	}

	// The container decides, as it would for a shell over the same access.
	if _, err := sc.Create("/usr/local/bin/evil"); err == nil {
		t.Fatal("a write the container refuses must fail")
	}
	if _, err := sc.Open("/etc/nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file outside the mounts: %v", err)
	}
	if err := sc.Mkdir("/root/nope"); err == nil {
		t.Fatal("a mkdir the container refuses must fail")
	}
	// A missing parent reads as "no such file", as from a real server: PyCharm retries
	// "permission denied" forever.
	if err := sc.Mkdir("/home/someone/PycharmProjects/x"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mkdir below a missing parent: %v", err)
	}
	if err := sc.Mkdir("/var"); err == nil {
		t.Fatal("the virtual directories above the mounts are not writable")
	}
	if err := sc.Rename("/tmp/a", "/var/www/html/a"); err == nil {
		t.Fatal("a move across the mount boundary must fail")
	}

	wd, err := sc.Getwd()
	if err != nil || wd != "/home/envoryx" {
		t.Fatalf("start directory: %q %v", wd, err)
	}
	if err := sc.Mkdir(".phpstorm_helpers"); err != nil {
		t.Fatalf("relative mkdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "projects", e.proj.Project.ID, "home", ".phpstorm_helpers")); err != nil {
		t.Fatalf("helpers dir not in the tool home: %v", err)
	}
	_, err = sc.Lstat("/home/envoryx/.phpstorm_helpers/build.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing helper: %v", err)
	}
	if strings.Contains(err.Error(), e.cfgDir) {
		t.Fatalf("error reveals the Envoryx-side path: %v", err)
	}
}

// IDEs connect first and ask for the password afterwards; a person typing it must not be
// cut off, while a client that never finishes still is.
func TestLoginWaitsForThePasswordPrompt(t *testing.T) {
	e := newEnv(t)
	slowPassword := func(d time.Duration) ssh.AuthMethod {
		return ssh.PasswordCallback(func() (string, error) { time.Sleep(d); return e.token, nil })
	}

	e.srv.SetLoginGrace(3 * time.Second)
	client, err := e.dial(t, "shop", slowPassword(time.Second))
	if err != nil {
		t.Fatalf("login within the grace time: %v", err)
	}
	client.Close()

	e.srv.SetLoginGrace(300 * time.Millisecond)
	if client, err := e.dial(t, "shop", slowPassword(time.Second)); err == nil {
		client.Close()
		t.Fatal("a login that outlasts the grace time must be dropped")
	}
}
