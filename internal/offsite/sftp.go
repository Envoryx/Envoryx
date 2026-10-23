package offsite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sftpBackend struct {
	ssh    *ssh.Client // nil in tests
	c      *sftp.Client
	prefix string
}

func parsePrivateKey(pem string) (ssh.Signer, error) {
	return ssh.ParsePrivateKey([]byte(strings.TrimSpace(pem) + "\n"))
}

// ErrHostKeyChanged is returned when an SFTP server presents a key other than the pinned
// one: either it's a different server, or someone is in between.
var ErrHostKeyChanged = errors.New("the SFTP server's host key changed")

func dialSFTP(ctx context.Context, t Target, pin func(string)) (Backend, error) {
	var auth []ssh.AuthMethod
	if t.PrivateKey != "" {
		signer, err := parsePrivateKey(t.PrivateKey)
		if err != nil {
			return nil, err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		auth = append(auth, ssh.Password(t.Password))
	}
	pinned, err := parseHostKey(t.HostKey)
	if err != nil {
		return nil, err
	}
	if pinned.empty() || pinned.types != nil {
		return sshConnect(ctx, t, auth, pinned, pinned.algorithms(), pin, nil)
	}
	// Only fingerprints, so the key type is unknown: the server's favourite key first, then
	// each other type it may hold.
	var seen []ssh.PublicKey
	b, err := sshConnect(ctx, t, auth, pinned, nil, pin, &seen)
	if !errors.Is(err, ErrHostKeyChanged) {
		return b, err
	}
	for _, kt := range hostKeyTypes {
		if slices.ContainsFunc(seen, func(k ssh.PublicKey) bool { return k.Type() == kt }) {
			continue
		}
		b, rerr := sshConnect(ctx, t, auth, pinned, keyAlgorithms(kt), pin, &seen)
		if rerr == nil {
			return b, nil
		}
		if !errors.Is(rerr, ErrHostKeyChanged) && !strings.Contains(rerr.Error(), "no common algorithm") {
			return nil, rerr
		}
	}
	return nil, hostKeyMismatch(pinned, seen)
}

// sshConnect opens the SFTP session, asking for the host key algorithms algos (nil for the
// defaults). The keys the server showed are added to seen.
func sshConnect(ctx context.Context, t Target, auth []ssh.AuthMethod, pinned hostKeyPin, algos []string, pin func(string), seen *[]ssh.PublicKey) (Backend, error) {
	cfg := &ssh.ClientConfig{
		User:              t.User,
		Auth:              auth,
		HostKeyAlgorithms: algos,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if seen != nil {
				*seen = append(*seen, key)
			}
			fp := ssh.FingerprintSHA256(key)
			if pinned.empty() {
				if pin != nil {
					pin(fp)
				}
				return nil
			}
			if !pinned.matches(fp) {
				return hostKeyMismatch(pinned, []ssh.PublicKey{key})
			}
			return nil
		},
		Timeout: 30 * time.Second,
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(sc, chans, reqs)
	c, err := sftp.NewClient(client, sftp.UseConcurrentWrites(true))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("the server does not offer SFTP: %w", err)
	}
	return &sftpBackend{ssh: client, c: c, prefix: t.Prefix}, nil
}

func (b *sftpBackend) path(key string) string { return b.prefix + "/" + key }

func (b *sftpBackend) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	full := b.path(key)
	if err := b.c.MkdirAll(path.Dir(full)); err != nil {
		return 0, fmt.Errorf("create %s: %w", path.Dir(full), err)
	}
	// Written under a temporary name and renamed, so an interrupted upload never looks
	// like a finished archive.
	tmp := full + ".part"
	f, err := b.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return 0, err
	}
	n, err := f.ReadFrom(r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = b.c.Remove(tmp)
		return 0, err
	}
	_ = b.c.Remove(full)
	if err := b.c.Rename(tmp, full); err != nil {
		return 0, err
	}
	return n, nil
}

func (b *sftpBackend) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := b.c.Open(b.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrObjectNotFound
	}
	return f, err
}

func (b *sftpBackend) List(_ context.Context, dir string) ([]Object, error) {
	entries, err := b.c.ReadDir(b.path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		out = append(out, Object{Key: dir + "/" + e.Name(), Size: e.Size(), Modified: e.ModTime()})
	}
	return out, nil
}

func (b *sftpBackend) Dirs(_ context.Context, dir string) ([]string, error) {
	entries, err := b.c.ReadDir(b.path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (b *sftpBackend) Delete(_ context.Context, key string) error {
	err := b.c.Remove(b.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (b *sftpBackend) Close() error {
	err := b.c.Close()
	if b.ssh != nil {
		_ = b.ssh.Close()
	}
	return err
}
