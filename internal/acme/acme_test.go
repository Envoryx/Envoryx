package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/tlsca"
)

// fakeCloudflare implements enough of the Cloudflare v4 API for the provider.
type fakeCloudflare struct {
	mu      sync.Mutex
	records map[string]map[string]string // id -> {name, content}
	seq     int
	token   string
	deleted int
}

func (f *fakeCloudflare) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`))
			return false
		}
		return true
	}
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		name := r.URL.Query().Get("name")
		if name == "example.com" {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"zone1","name":"example.com"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	})
	mux.HandleFunc("POST /zones/zone1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body struct{ Type, Name, Content string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.seq++
		id := fmt.Sprintf("rec%d", f.seq)
		f.records[id] = map[string]string{"name": body.Name, "content": body.Content, "type": body.Type}
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"success":true,"errors":[],"result":{"id":%q}}`, id)
	})
	mux.HandleFunc("DELETE /zones/zone1/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		f.mu.Lock()
		delete(f.records, r.PathValue("id"))
		f.deleted++
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	})
	return mux
}

func (f *fakeCloudflare) txt(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.records {
		if r["type"] == "TXT" && r["name"] == name {
			out = append(out, r["content"])
		}
	}
	return out
}

// fakeACME is a minimal RFC 8555 server: it trusts every JWS, expects two TXT records
// for the challenge name before validating, and signs the CSR with a test CA.
type fakeACME struct {
	url   string
	cf    *fakeCloudflare
	caKey *ecdsa.PrivateKey
	ca    *x509.Certificate
	mu    sync.Mutex
	valid map[string]bool
}

func jwsPayload(r *http.Request) []byte {
	var env struct{ Payload string }
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &env)
	b, _ := base64.RawURLEncoding.DecodeString(env.Payload)
	return b
}

func (a *fakeACME) handler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Replay-Nonce", "nonce-"+fmt.Sprint(time.Now().UnixNano()))
			w.Header().Set("Content-Type", "application/json")
			h(w, r)
		}
	}
	mux.HandleFunc("/directory", wrap(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"newNonce":%q,"newAccount":%q,"newOrder":%q}`, a.url+"/nonce", a.url+"/account", a.url+"/order")
	}))
	mux.HandleFunc("/nonce", wrap(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	mux.HandleFunc("/account", wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", a.url+"/acct/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	}))
	mux.HandleFunc("/order", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Identifiers []struct{ Type, Value string }
		}
		_ = json.Unmarshal(jwsPayload(r), &req)
		if len(req.Identifiers) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", a.url+"/order/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"status":"pending","identifiers":[{"type":"dns","value":%q},{"type":"dns","value":%q}],"authorizations":[%q,%q],"finalize":%q}`,
			req.Identifiers[0].Value, req.Identifiers[1].Value, a.url+"/authz/1", a.url+"/authz/2", a.url+"/finalize")
	}))
	mux.HandleFunc("/authz/{n}", wrap(func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("n")
		name, wildcard := "dev.example.com", "false"
		if n == "2" {
			wildcard = "true"
		}
		a.mu.Lock()
		status := "pending"
		if a.valid[n] {
			status = "valid"
		}
		a.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"status":%q,"identifier":{"type":"dns","value":%q},"wildcard":%s,"challenges":[{"type":"dns-01","url":%q,"token":"tok%s","status":%q}]}`,
			status, name, wildcard, a.url+"/chal/"+n, n, status)
	}))
	mux.HandleFunc("/chal/{n}", wrap(func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("n")
		if len(a.cf.txt("_acme-challenge.dev.example.com")) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:dns","detail":"TXT records missing"}`))
			return
		}
		a.mu.Lock()
		a.valid[n] = true
		a.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"type":"dns-01","url":%q,"token":"tok%s","status":"valid"}`, a.url+"/chal/"+n, n)
	}))
	mux.HandleFunc("/finalize", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ CSR string }
		_ = json.Unmarshal(jwsPayload(r), &req)
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: csr.DNSNames[0]}, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, a.ca, csr.PublicKey, a.caKey)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		a.mu.Lock()
		a.valid["cert"] = true
		a.valid["certPEM"] = true
		a.mu.Unlock()
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
		w.Header().Set("Location", a.url+"/order/1")
		_, _ = fmt.Fprintf(w, `{"status":"valid","certificate":%q,"finalize":%q}`, a.url+"/cert", a.url+"/finalize")
	}))
	mux.HandleFunc("/order/1", wrap(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"status":"valid","certificate":%q,"finalize":%q}`, a.url+"/cert", a.url+"/finalize")
	}))
	mux.HandleFunc("/cert", wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(certPEM)
	}))
	return mux
}

var certPEM []byte

func newFakeACME(t *testing.T, cf *fakeCloudflare) *fakeACME {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake LE"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	ca, _ := x509.ParseCertificate(der)
	a := &fakeACME{cf: cf, caKey: key, ca: ca, valid: map[string]bool{}}
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)
	a.url = srv.URL
	return a
}

func TestIssueObtainsWildcardThroughDNSChallenge(t *testing.T) {
	cf := &fakeCloudflare{records: map[string]map[string]string{}, token: "cf-secret"}
	cfSrv := httptest.NewServer(cf.handler())
	defer cfSrv.Close()
	fake := newFakeACME(t, cf)

	certs, err := tlsca.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(t.TempDir(), certs, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.directoryURL = fake.url + "/directory"
	m.providerBase = cfSrv.URL
	m.lookupTXT = func(_ context.Context, fqdn string) ([]string, error) { return cf.txt(fqdn), nil }

	if err := m.SetConfig(Config{Provider: "gandi", Domain: "dev.example.com", Email: "me@example.com", Token: "x"}); err == nil {
		t.Fatal("unsupported provider must be rejected")
	}
	if err := m.SetConfig(Config{Provider: "cloudflare", Domain: "dev.example.com", Email: "me@example.com"}); err == nil {
		t.Fatal("missing token must be rejected on first configuration")
	}
	if err := m.SetConfig(Config{Provider: "cloudflare", Domain: "Dev.Example.com", Email: "me@example.com", Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	if !m.NeedsIssue() {
		t.Fatal("must need a certificate")
	}
	if err := m.Issue(context.Background()); err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Fatalf("wrong token must fail with a helpful error: %v", err)
	}
	if st := m.Status(); st.LastError == "" || st.NotAfter != nil {
		t.Fatalf("status after failure: %+v", st)
	}

	// Empty token keeps the stored one; fix it with the real value.
	if err := m.SetConfig(Config{Provider: "cloudflare", Domain: "dev.example.com", Email: "me@example.com", Token: "cf-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Issue(context.Background()); err != nil {
		t.Fatalf("issue: %v", err)
	}
	st := m.Status()
	if st.NotAfter == nil || st.LastError != "" || !covers(st.Names, "dev.example.com") || st.LastSuccess == nil {
		t.Fatalf("status after success: %+v", st)
	}
	if cf.deleted != 2 || len(cf.records) != 0 {
		t.Fatalf("challenge records must be cleaned up: deleted=%d left=%d", cf.deleted, len(cf.records))
	}
	if m.NeedsIssue() {
		t.Fatal("fresh certificate must not need renewal")
	}
	// The certificate is served for names under the domain.
	if c, err := certs.Certificate("shop.dev.example.com"); err != nil || c.Leaf.Subject.CommonName != "dev.example.com" {
		t.Fatalf("custom cert must be used: %v", err)
	}
	// Renewal kicks in 30 days before expiry.
	m.now = func() time.Time { return time.Now().Add(65 * 24 * time.Hour) }
	if !m.NeedsIssue() {
		t.Fatal("must renew before expiry")
	}

	// Configuration survives a restart (token included) and Clear removes everything.
	m2, err := New(m.dir, certs, m.log)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Config().Credentials["token"] != "cf-secret" || m2.Status().Domain != "dev.example.com" {
		t.Fatalf("config not persisted: %+v", m2.Status())
	}
	if err := m2.Clear(); err != nil {
		t.Fatal(err)
	}
	if m2.Status().Configured || certs.Info().Custom != nil {
		t.Fatal("clear must remove config and certificate")
	}
}
