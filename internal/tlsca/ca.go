// Package tlsca provides Envoryx's local certificate authority: a self-signed CA generated
// once under /config/ca, leaf certificates issued on demand per hostname, and an optional
// operator-supplied certificate (e.g. a Let's Encrypt wildcard) that takes precedence for
// the hostnames it covers.
package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	caKeyFile    = "ca.key"
	caCertFile   = "ca.crt"
	certsDir     = "certs"
	customCert   = "custom.crt"
	customKey    = "custom.key"
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 397 * 24 * time.Hour // browsers reject longer-lived leaf certificates
	renewBefore  = 30 * 24 * time.Hour
	caCommonName = "Envoryx Local CA"
	caOrgName    = "Envoryx"
)

// Store issues and caches certificates.
type Store struct {
	dir string
	mu  sync.Mutex

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte

	custom *tls.Certificate // operator supplied; nil if absent
	leafs  map[string]*tls.Certificate
}

// Open loads (or creates) the CA under dir and loads a custom certificate if present.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, leafs: map[string]*tls.Certificate{}}
	if err := os.MkdirAll(filepath.Join(dir, certsDir), 0o700); err != nil {
		return nil, fmt.Errorf("create ca directory: %w", err)
	}
	if err := s.loadOrCreateCA(); err != nil {
		return nil, err
	}
	if err := s.loadCustom(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadOrCreateCA() error {
	keyPEM, keyErr := os.ReadFile(filepath.Join(s.dir, caKeyFile))
	certPEM, certErr := os.ReadFile(filepath.Join(s.dir, caCertFile))
	if keyErr == nil && certErr == nil {
		key, err := parseECKey(keyPEM)
		if err != nil {
			return fmt.Errorf("ca key: %w", err)
		}
		cert, err := parseCert(certPEM)
		if err != nil {
			return fmt.Errorf("ca cert: %w", err)
		}
		s.caKey, s.caCert, s.caPEM = key, cert, certPEM
		return nil
	}
	if !errors.Is(keyErr, os.ErrNotExist) && keyErr != nil {
		return keyErr
	}
	return s.createCA()
}

func (s *Store) createCA() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName, Organization: []string{caOrgName}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(s.dir, caKeyFile), keyPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.dir, caCertFile), certPEM, 0o644); err != nil {
		return err
	}
	s.caKey, s.caCert, s.caPEM = key, cert, certPEM
	return nil
}

func (s *Store) loadCustom() error {
	certPath, keyPath := filepath.Join(s.dir, customCert), filepath.Join(s.dir, customKey)
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		s.custom = nil
		return nil
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("custom certificate: %w", err)
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	s.custom = &cert
	return nil
}

// CAPEM returns the CA certificate for download.
func (s *Store) CAPEM() []byte { return s.caPEM }

// Info describes the CA and custom certificate for the UI.
type Info struct {
	CASubject     string      `json:"caSubject"`
	CAFingerprint string      `json:"caFingerprint"` // SHA-256, colon separated
	CANotAfter    time.Time   `json:"caNotAfter"`
	Custom        *CustomInfo `json:"custom,omitempty"`
}

// CustomInfo describes an operator-supplied certificate.
type CustomInfo struct {
	Subject  string    `json:"subject"`
	DNSNames []string  `json:"dnsNames"`
	NotAfter time.Time `json:"notAfter"`
	Issuer   string    `json:"issuer"`
	Expired  bool      `json:"expired"`
}

// Info returns metadata for the UI.
func (s *Store) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := sha256.Sum256(s.caCert.Raw)
	fp := hex.EncodeToString(sum[:])
	var parts []string
	for i := 0; i < len(fp); i += 2 {
		parts = append(parts, strings.ToUpper(fp[i:i+2]))
	}
	info := Info{CASubject: s.caCert.Subject.CommonName, CAFingerprint: strings.Join(parts, ":"), CANotAfter: s.caCert.NotAfter}
	if s.custom != nil && s.custom.Leaf != nil {
		l := s.custom.Leaf
		info.Custom = &CustomInfo{Subject: l.Subject.CommonName, DNSNames: l.DNSNames, NotAfter: l.NotAfter, Issuer: l.Issuer.CommonName, Expired: time.Now().After(l.NotAfter)}
	}
	return info
}

// SetCustom validates and stores an operator-supplied certificate chain and key (PEM).
// Passing empty PEM removes the custom certificate.
func (s *Store) SetCustom(certPEM, keyPEM []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	certPath, keyPath := filepath.Join(s.dir, customCert), filepath.Join(s.dir, customKey)
	if len(certPEM) == 0 {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		s.custom = nil
		return nil
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("certificate and key do not match or are malformed: %w", err)
	}
	if cert.Leaf == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return err
		}
	}
	if len(cert.Leaf.DNSNames) == 0 && cert.Leaf.Subject.CommonName == "" {
		return errors.New("certificate has no DNS names")
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	s.custom = &cert
	return nil
}

// Certificate returns the certificate to present for a host: the custom certificate if it
// covers the name, otherwise a CA-issued leaf (cached on disk and in memory).
func (s *Store) Certificate(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("empty server name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.custom != nil && s.custom.Leaf != nil && s.custom.Leaf.VerifyHostname(host) == nil {
		return s.custom, nil
	}
	if c, ok := s.leafs[host]; ok && time.Until(c.Leaf.NotAfter) > renewBefore {
		return c, nil
	}
	if c, err := s.loadLeaf(host); err == nil && time.Until(c.Leaf.NotAfter) > renewBefore {
		s.leafs[host] = c
		return c, nil
	}
	c, err := s.issue(host)
	if err != nil {
		return nil, err
	}
	s.leafs[host] = c
	return c, nil
}

func (s *Store) leafPath(host string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, host)
	return filepath.Join(s.dir, certsDir, safe+".pem")
}

func (s *Store) loadLeaf(host string) (*tls.Certificate, error) {
	data, err := os.ReadFile(s.leafPath(host))
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, err
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if cert.Leaf.VerifyHostname(host) != nil {
		return nil, errors.New("cached certificate does not match host")
	}
	return &cert, nil
}

func (s *Store) issue(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{caOrgName}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.caCert, &key.PublicKey, s.caKey)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	pemData := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	if err := os.WriteFile(s.leafPath(host), pemData, 0o600); err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, s.caCert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// TLSConfig returns a server TLS configuration that issues certificates on the fly. allow
// decides whether a certificate may be issued for the requested server name.
func (s *Store) TLSConfig(allow func(host string) bool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			host := strings.ToLower(hello.ServerName)
			if host == "" {
				if hello.Conn != nil {
					if addr, ok := hello.Conn.LocalAddr().(*net.TCPAddr); ok {
						host = addr.IP.String()
					}
				}
			}
			if host == "" || !allow(host) {
				return nil, fmt.Errorf("no certificate for %q", host)
			}
			return s.Certificate(host)
		},
	}
}

func parseECKey(pemData []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func parseCert(pemData []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}
