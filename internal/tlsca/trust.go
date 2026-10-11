package tlsca

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The trust files for the project containers. An application that calls its own address
// (webhooks, cache warmers, OAuth back-channels) reaches the proxy, which answers with a
// certificate of the local CA; the runtimes only accept it when they trust the CA.
const (
	// TrustCAFile is the bare CA certificate, for runtimes that add certificates to their
	// own store (Node's NODE_EXTRA_CA_CERTS).
	TrustCAFile = "envoryx-ca.crt"
	// TrustBundleFile is the system bundle plus the CA, for runtimes that take one file in
	// place of the system store (OpenSSL's SSL_CERT_FILE, Python requests, curl, PHP).
	TrustBundleFile = "ca-bundle.crt"
)

// systemBundles are the places Linux distributions keep the system CA bundle, in the order
// Go looks for them.
var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL
	"/etc/ssl/ca-bundle.pem",                            // openSUSE
	"/etc/pki/tls/cacert.pem",                           // OpenELEC
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS, RHEL 7
	"/etc/ssl/cert.pem",                                 // Alpine Linux, macOS
}

// ErrNoSystemBundle means no system CA bundle was found to build the trust bundle from. A
// bundle with the local CA alone would make every public site untrusted in the containers.
var ErrNoSystemBundle = errors.New("no system CA bundle found")

// WriteTrust writes the trust files into dir: the CA certificate and a bundle of the
// system's CAs (Envoryx's own, from its container image) plus the local CA. Each file is
// replaced by a rename, so the containers mount the directory, never a single file: a
// bind-mounted file would keep showing the old one.
func (s *Store) WriteTrust(dir string) error {
	return s.writeTrust(dir, systemBundles)
}

func (s *Store) writeTrust(dir string, bundles []string) error {
	var system []byte
	for _, path := range bundles {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			system = b
			break
		}
	}
	if system == nil {
		return ErrNoSystemBundle
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create trust directory: %w", err)
	}
	bundle := append([]byte{}, system...)
	if bundle[len(bundle)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	bundle = append(append(bundle, "# Envoryx Local CA\n"...), s.caPEM...)
	if err := writeAtomic(filepath.Join(dir, TrustCAFile), s.caPEM); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, TrustBundleFile), bundle)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
