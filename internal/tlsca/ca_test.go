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
