package tlsca

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCAIssuesAndCachesCertificates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "ca.key")); info.Mode().Perm() != 0o600 {
		t.Fatalf("ca key mode %o", info.Mode().Perm())
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(s.CAPEM()) {
		t.Fatal("ca pem invalid")
	}
	cert, err := s.Certificate("shop.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{DNSName: "shop.test", Roots: roots}); err != nil {
		t.Fatalf("leaf must chain to the CA: %v", err)
	}
	if cert.Leaf.NotAfter.Sub(time.Now()) > 398*24*time.Hour {
		t.Fatal("leaf validity too long for browsers")
	}
	again, _ := s.Certificate("SHOP.test.")
	if again != cert {
		t.Fatal("certificate must be cached (case/trailing dot normalised)")
	}
	ipCert, err := s.Certificate("192.168.1.10")
	if err != nil || len(ipCert.Leaf.IPAddresses) != 1 {
		t.Fatalf("ip certificate: %v %+v", err, ipCert)
	}

	// Reopening reuses the CA and the cached leaf from disk.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(s2.CAPEM()) != string(s.CAPEM()) {
		t.Fatal("CA must persist")
	}
	c2, _ := s2.Certificate("shop.test")
	if c2.Leaf.SerialNumber.Cmp(cert.Leaf.SerialNumber) != 0 {
		t.Fatal("leaf must be loaded from disk, not re-issued")
	}
	if s.Info().CAFingerprint == "" || s.Info().Custom != nil {
		t.Fatalf("info: %+v", s.Info())
	}
	tlsCfg := s.TLSConfig(func(h string) bool { return h == "shop.test" })
	if tlsCfg.GetCertificate == nil {
		t.Fatal("tls config")
	}
}

func TestCustomCertificateWinsForCoveredNames(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Use a second store's CA to mint a "custom" wildcard cert.
	other, _ := Open(t.TempDir())
	wild, err := other.Certificate("*.dev.example.com")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(other.leafPath("*.dev.example.com"))
	if err := s.SetCustom(certPEM, certPEM); err != nil {
		t.Fatal(err)
	}
	got, err := s.Certificate("shop.dev.example.com")
	if err != nil || got.Leaf.SerialNumber.Cmp(wild.Leaf.SerialNumber) != 0 {
		t.Fatalf("custom certificate must be used for covered names: %v", err)
	}
	local, _ := s.Certificate("shop.test")
	if local.Leaf.SerialNumber.Cmp(wild.Leaf.SerialNumber) == 0 {
		t.Fatal("uncovered names must fall back to the local CA")
	}
	if s.Info().Custom == nil || s.Info().Custom.DNSNames[0] != "*.dev.example.com" {
		t.Fatalf("custom info: %+v", s.Info())
	}
	if err := s.SetCustom([]byte("garbage"), []byte("garbage")); err == nil {
		t.Fatal("malformed custom certificate must be rejected")
	}
	if err := s.SetCustom(nil, nil); err != nil || s.Info().Custom != nil {
		t.Fatal("clearing the custom certificate failed")
	}
}

func TestPruneRemovesOnlyUnwantedLeavesOfThisCA(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	blog, err := s.Certificate("blog.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"shop.test", "192.168.1.10"} {
		if _, err := s.Certificate(h); err != nil {
			t.Fatal(err)
		}
	}
	certs := filepath.Join(dir, "certs")
	blogPEM, _ := os.ReadFile(s.leafPath("blog.test"))
	other, _ := Open(t.TempDir())
	if _, err := other.Certificate("gone.test"); err != nil {
		t.Fatal(err)
	}
	foreignPEM, _ := os.ReadFile(other.leafPath("gone.test"))
	// Files Prune must leave alone: not a certificate, another CA's leaf, a leaf under
	// another name than its own.
	for name, data := range map[string][]byte{"notes.pem": []byte("hello"), "gone.test.pem": foreignPEM, "copy.pem": blogPEM} {
		if err := os.WriteFile(filepath.Join(certs, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.Prune(func(h string) bool { return h == "shop.test" })
	if err != nil || len(removed) != 1 || removed[0] != "blog.test" {
		t.Fatalf("removed = %v, %v", removed, err)
	}
	for _, name := range []string{"shop.test.pem", "192.168.1.10.pem", "notes.pem", "gone.test.pem", "copy.pem"} {
		if _, err := os.Stat(filepath.Join(certs, name)); err != nil {
			t.Errorf("%s must stay: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(certs, "blog.test.pem")); !os.IsNotExist(err) {
		t.Fatalf("blog.test.pem must be gone: %v", err)
	}
	// The memory cache forgets it too: the next visit gets a fresh certificate.
	again, err := s.Certificate("blog.test")
	if err != nil || again.Leaf.SerialNumber.Cmp(blog.Leaf.SerialNumber) == 0 {
		t.Fatalf("blog.test after prune: %v", err)
	}
}
