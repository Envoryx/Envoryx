package offsite

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sshServer serves SFTP over SSH with the given host keys, like a storage box offering
// ECDSA and Ed25519 keys at once.
func sshServer(t *testing.T, keys ...ssh.Signer) (host string, port int) {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) != "secret" {
			return nil, errors.New("wrong password")
		}
		return nil, nil
	}}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, cfg)
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range creqs {
				ok := r.Type == "subsystem" && strings.HasSuffix(string(r.Payload), "sftp")
				_ = r.Reply(ok, nil)
				if ok {
					srv := sftp.NewRequestServer(ch, sftp.InMemHandler())
					_ = srv.Serve()
					ch.Close()
				}
			}
		}()
	}
}

func hostKeys(t *testing.T) (ed, ec ssh.Signer) {
	t.Helper()
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ed, _ = ssh.NewSignerFromKey(edKey)
	ec, _ = ssh.NewSignerFromKey(ecKey)
	return ed, ec
}

func TestSFTPHostKeyPinWithSeveralServerKeys(t *testing.T) {
	ed, ec := hostKeys(t)
	host, port := sshServer(t, ec, ed)
	keyscan := func(s ssh.Signer) string {
		return "[" + host + "]:" + strconv.Itoa(port) + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	}
	_, other := hostKeys(t)

	for _, tc := range []struct {
		name, pin string
		ok        bool
	}{
		{"keyscan line of the Ed25519 key", keyscan(ed), true},
		{"bare Ed25519 key", string(ssh.MarshalAuthorizedKey(ed.PublicKey())), true},
		{"Ed25519 fingerprint", ssh.FingerprintSHA256(ed.PublicKey()), true},
		{"ECDSA fingerprint", ssh.FingerprintSHA256(ec.PublicKey()), true},
		{"ssh-keygen -l output", "256 " + ssh.FingerprintSHA256(ed.PublicKey()) + " " + host + " (ED25519)", true},
		{"full keyscan output", "# " + host + " SSH-2.0\n" + keyscan(ec) + "\n" + keyscan(ed) + "\n", true},
		{"another server's fingerprint", ssh.FingerprintSHA256(other.PublicKey()), false},
		{"another server's key", string(ssh.MarshalAuthorizedKey(other.PublicKey())), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := Target{Name: "Box", Type: TypeSFTP, Host: host, Port: port, User: "backup", Password: "secret", HostKey: tc.pin, Prefix: "envoryx"}
			if err := target.normalize(); err != nil {
				t.Fatal(err)
			}
			b, err := dialSFTP(context.Background(), target, func(string) { t.Error("a pinned key must not be replaced") })
			if !tc.ok {
				if !errors.Is(err, ErrHostKeyChanged) {
					t.Fatalf("want a changed host key, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if _, err := b.List(context.Background(), "projects"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSFTPHostKeyPinnedOnFirstConnection(t *testing.T) {
	ed, ec := hostKeys(t)
	host, port := sshServer(t, ec, ed)
	var pinned string
	b, err := dialSFTP(context.Background(), Target{Type: TypeSFTP, Host: host, Port: port, User: "backup", Password: "secret"}, func(fp string) { pinned = fp })
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if pinned != ssh.FingerprintSHA256(ec.PublicKey()) && pinned != ssh.FingerprintSHA256(ed.PublicKey()) {
		t.Fatalf("pinned %q", pinned)
	}
}

func TestSFTPHostKeyFormat(t *testing.T) {
	for _, bad := range []string{"SHA256:short", "ssh-ed25519 notbase64", "just words"} {
		target := Target{Name: "Box", Type: TypeSFTP, Host: "example.com", User: "u", Password: "p", HostKey: bad}
		if err := target.normalize(); err == nil || !strings.Contains(err.Error(), "server key") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
