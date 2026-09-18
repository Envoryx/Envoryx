package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDomainsAndProxySettings(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	id := p["id"].(string)
	if hosts := p["hostnames"].([]any); len(hosts) != 1 || hosts[0] != "shop.test" {
		t.Fatalf("default hostname: %v", hosts)
	}

	// Add, list, reject, remove.
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/domains", map[string]any{"hostname": "Shop.Local."}, true)
	if r.status != http.StatusCreated || r.body["domain"].(map[string]any)["hostname"] != "shop.local" {
		t.Fatalf("add domain: %d %s", r.status, r.raw)
	}
	domainID := r.body["domain"].(map[string]any)["id"].(string)
	for _, bad := range []string{"", "staqio.test", "shop.test", "*.shop.local", "shop.local", "has space.test"} {
		r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/domains", map[string]any{"hostname": bad}, true)
		if r.status < 400 {
			t.Fatalf("hostname %q must be rejected: %d %s", bad, r.status, r.raw)
		}
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/domains", nil, false)
	if r.status != http.StatusOK || len(r.body["domains"].([]any)) != 2 {
		t.Fatalf("list domains: %d %s", r.status, r.raw)
	}
	if px := r.body["proxy"].(map[string]any); px["enabled"] != true || px["httpsPort"].(float64) != 443 || px["tls"] != true {
		t.Fatalf("proxy info: %v", px)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	if hosts := r.body["project"].(map[string]any)["hostnames"].([]any); len(hosts) != 2 || hosts[1] != "shop.local" {
		t.Fatalf("project hostnames: %v", hosts)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/domains/"+domainID, nil, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("remove domain: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/domains/"+domainID, nil, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("removing twice must be 404: %d %s", r.status, r.raw)
	}

	// Base domain + force HTTPS settings.
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"baseDomain": "Dev.Home", "forceHttps": true}, true)
	if r.status != http.StatusOK || r.body["baseDomain"] != "dev.home" || r.body["forceHttps"] != true {
		t.Fatalf("settings: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"baseDomain": "bad domain"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid base domain must fail: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	if hosts := r.body["project"].(map[string]any)["hostnames"].([]any); hosts[0] != "shop.dev.home" {
		t.Fatalf("hostnames must follow the base domain: %v", hosts)
	}
}

func TestTLSEndpoints(t *testing.T) {
	a := newApp(t)

	// CA download and TLS info require a session.
	r := a.do(http.MethodGet, "/api/v1/settings/tls/ca.crt", nil, false)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("ca.crt must require login: %d", r.status)
	}
	a.setupAndLogin()
	r = a.do(http.MethodGet, "/api/v1/settings/tls/ca.crt", nil, false)
	if r.status != http.StatusOK || !strings.HasPrefix(string(r.raw), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("ca.crt: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/settings/tls", nil, false)
	if r.status != http.StatusOK || r.body["enabled"] != true || r.body["ca"].(map[string]any)["caFingerprint"] == "" {
		t.Fatalf("tls info: %d %s", r.status, r.raw)
	}
	if r.body["ca"].(map[string]any)["custom"] != nil {
		t.Fatalf("no custom certificate expected: %s", r.raw)
	}

	certPEM, keyPEM := selfSigned(t, "*.dev.example.com")
	r = a.do(http.MethodPut, "/api/v1/settings/tls/custom", map[string]any{"certificate": certPEM, "key": "garbage"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("broken key must be rejected: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/settings/tls/custom", map[string]any{"certificate": certPEM, "key": keyPEM}, true)
	if r.status != http.StatusOK {
		t.Fatalf("set custom: %d %s", r.status, r.raw)
	}
	custom := r.body["ca"].(map[string]any)["custom"].(map[string]any)
	if custom["dnsNames"].([]any)[0] != "*.dev.example.com" {
		t.Fatalf("custom info: %v", custom)
	}
	r = a.do(http.MethodDelete, "/api/v1/settings/tls/custom", nil, true)
	if r.status != http.StatusOK || r.body["ca"].(map[string]any)["custom"] != nil {
		t.Fatalf("clear custom: %d %s", r.status, r.raw)
	}
}

func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}
