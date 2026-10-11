package tlsca

import (
	"bytes"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The bundle is the system bundle with the local CA added: a leaf of the CA verifies
// against it, and what the system trusted stays in it.
func TestWriteTrustBundlesSystemAndLocalCA(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	system := filepath.Join(t.TempDir(), "system.pem")
	// No trailing newline: the CA must still start on its own line.
	if err := os.WriteFile(system, bytes.TrimRight(other.CAPEM(), "\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "trust")
	if err := s.writeTrust(dir, []string{filepath.Join(t.TempDir(), "missing.pem"), system}); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, TrustCAFile))
	if err != nil || !bytes.Equal(ca, s.CAPEM()) {
		t.Fatalf("ca file: %v %q", err, ca)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, TrustBundleFile))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("bundle holds no certificates")
	}
	for name, store := range map[string]*Store{"local": s, "system": other} {
		leaf, err := store.Certificate("shop.test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "shop.test", Roots: roots}); err != nil {
			t.Errorf("%s CA not trusted by the bundle: %v", name, err)
		}
	}
	if info, _ := os.Stat(filepath.Join(dir, TrustBundleFile)); info.Mode().Perm() != 0o644 {
		t.Errorf("bundle mode %o", info.Mode().Perm())
	}
	// Written again (every start) it replaces the files and leaves no temporary ones.
	if err := s.writeTrust(dir, []string{system}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("trust directory: %v", entries)
	}
}

func TestWriteTrustNeedsASystemBundle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeTrust(t.TempDir(), []string{"/nonexistent/bundle.pem"}); !errors.Is(err, ErrNoSystemBundle) {
		t.Fatalf("err = %v", err)
	}
}
