package plan

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CertificateFile is the hoster's certificate in the config directory. Its fleet manager
// obtains a wildcard certificate for the instance's domain and keeps it renewed, so the
// DNS credentials stay with the hoster; without the file the instance gets its
// certificates itself.
const CertificateFile = "certificate.json"

// Certificate is the wildcard certificate a hoster provides for an instance: Domain and
// *.Domain, with its chain and key (PEM).
type Certificate struct {
	Domain      string `json:"domain"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// ParseCertificate reads and checks a hoster's certificate: the key fits, and the
// certificate covers the domain and every name below it.
func ParseCertificate(b []byte) (*Certificate, error) {
	var c Certificate
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	c.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	if c.Domain == "" || !strings.Contains(c.Domain, ".") {
		return nil, errors.New("certificate: the domain is missing")
	}
	leaf, err := c.Leaf()
	if err != nil {
		return nil, err
	}
	for _, host := range []string{c.Domain, "envoryx." + c.Domain} {
		if leaf.VerifyHostname(host) != nil {
			return nil, fmt.Errorf("certificate: it doesn't cover %s", host)
		}
	}
	return &c, nil
}

// Leaf returns the certificate itself, after checking that the key fits.
func (c *Certificate) Leaf() (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	if err != nil {
		return nil, fmt.Errorf("certificate: certificate and key don't fit together: %w", err)
	}
	return x509.ParseCertificate(pair.Certificate[0])
}

// NotAfter is when the certificate expires; zero when it can't be read.
func (c *Certificate) NotAfter() time.Time {
	if c == nil {
		return time.Time{}
	}
	leaf, err := c.Leaf()
	if err != nil {
		return time.Time{}
	}
	return leaf.NotAfter
}
