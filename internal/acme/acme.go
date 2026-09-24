// Package acme obtains and renews a public wildcard certificate for the project base
// domain through Let's Encrypt (dns-01 challenge), so browsers trust project URLs without
// installing Envoryx's local CA. The certificate is handed to the tlsca store as its
// "custom" certificate.
package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/tlsca"
	"github.com/envoryx/envoryx/internal/validate"
)

const (
	configFile     = "acme.json"
	accountKeyFile = "acme-account.key"
	stagingURL     = "https://acme-staging-v02.api.letsencrypt.org/directory"
	renewBefore    = 30 * 24 * time.Hour
	checkInterval  = 12 * time.Hour
	// issueSlack is the time an issuance takes besides waiting for the DNS record.
	issueSlack = 10 * time.Minute
)

// Config is the operator-supplied ACME configuration. Credentials holds the provider's
// fields (see ProviderList); the secret ones never leave the server.
type Config struct {
	Provider    string            `json:"provider"`
	Domain      string            `json:"domain"`
	Email       string            `json:"email"`
	Credentials map[string]string `json:"credentials,omitempty"`
	Staging     bool              `json:"staging"`
	// Token is the Cloudflare token of configurations written before providers had
	// several fields; it is read once and moved into Credentials.
	Token string `json:"token,omitempty"`
}

// Status describes the ACME state for the UI. The token is never included.
type Status struct {
	Configured  bool       `json:"configured"`
	Provider    string     `json:"provider,omitempty"`
	Domain      string     `json:"domain,omitempty"`
	Email       string     `json:"email,omitempty"`
	Staging     bool       `json:"staging,omitempty"`
	Issuing     bool       `json:"issuing"`
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	// NotAfter is the expiry of the current certificate for the domain (nil = none).
	NotAfter *time.Time `json:"notAfter,omitempty"`
	// Names are the DNS names of the current certificate.
	Names []string `json:"names,omitempty"`
	// Fields are the provider's non-secret credentials; Secrets names the secret ones
	// that are stored.
	Fields  map[string]string `json:"fields,omitempty"`
	Secrets []string          `json:"secrets,omitempty"`
}

// Manager holds the configuration and drives issuance/renewal.
type Manager struct {
	dir   string
	certs *tlsca.Store
	log   *slog.Logger

	mu          sync.Mutex
	cfg         Config
	issuing     bool
	lastAttempt *time.Time
	lastSuccess *time.Time
	lastError   string
	wake        chan struct{}

	notifier notify.Sender

	// Test hooks.
	directoryURL string
	httpClient   *http.Client
	providerBase string
	lookupTXT    func(ctx context.Context, fqdn string) ([]string, error)
	now          func() time.Time
}

// New loads the configuration from dir (created on demand).
func New(dir string, certs *tlsca.Store, log *slog.Logger) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create acme directory: %w", err)
	}
	m := &Manager{dir: dir, certs: certs, log: log, wake: make(chan struct{}, 1), now: time.Now}
	m.lookupTXT = authoritativeTXTLookup
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if err == nil {
		if err := json.Unmarshal(raw, &m.cfg); err != nil {
			log.Warn("acme configuration unreadable; ignoring", "err", err)
			m.cfg = Config{}
		}
		if m.cfg.Token != "" && m.cfg.Credentials == nil {
			m.cfg.Credentials = map[string]string{"token": m.cfg.Token}
		}
		m.cfg.Token = ""
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read acme configuration: %w", err)
	}
	return m, nil
}

// SetNotifier installs the notification sink.
func (m *Manager) SetNotifier(n notify.Sender) { m.notifier = n }

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Configured: m.cfg.Provider != "", Provider: m.cfg.Provider, Domain: m.cfg.Domain, Email: m.cfg.Email, Staging: m.cfg.Staging,
		Issuing: m.issuing, LastAttempt: m.lastAttempt, LastSuccess: m.lastSuccess, LastError: m.lastError}
	if p, ok := providerInfo(m.cfg.Provider); ok {
		for _, f := range p.Fields {
			v := m.cfg.Credentials[f.Key]
			switch {
			case v == "":
			case f.Secret:
				st.Secrets = append(st.Secrets, f.Key)
			default:
				if st.Fields == nil {
					st.Fields = map[string]string{}
				}
				st.Fields[f.Key] = v
			}
		}
	}
	if info := m.certs.Info(); info.Custom != nil && m.cfg.Domain != "" && covers(info.Custom.DNSNames, m.cfg.Domain) {
		na := info.Custom.NotAfter
		st.NotAfter = &na
		st.Names = info.Custom.DNSNames
	}
	return st
}

// Config returns a copy of the configuration (token included; for internal use).
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// SetConfig validates and stores a configuration. An empty secret field keeps the stored
// value as long as the provider stays the same.
func (m *Manager) SetConfig(cfg Config) error {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Domain = validate.NormalizeHostname(cfg.Domain)
	cfg.Email = strings.TrimSpace(cfg.Email)
	info, ok := providerInfo(cfg.Provider)
	if !ok {
		return fmt.Errorf("%w: unsupported DNS provider %q", validate.ErrInvalid, cfg.Provider)
	}
	// Older clients send the Cloudflare token on its own.
	if t := strings.TrimSpace(cfg.Token); t != "" {
		if cfg.Credentials == nil {
			cfg.Credentials = map[string]string{}
		}
		if cfg.Credentials["token"] == "" {
			cfg.Credentials["token"] = t
		}
	}
	cfg.Token = ""
	if err := validate.Hostname(cfg.Domain); err != nil {
		return err
	}
	if strings.Count(cfg.Domain, ".") < 1 {
		return fmt.Errorf("%w: domain must be a registrable name such as dev.example.com", validate.ErrInvalid)
	}
	if cfg.Email == "" || !strings.Contains(cfg.Email, "@") || strings.ContainsAny(cfg.Email, " \t\r\n") {
		return fmt.Errorf("%w: a contact e-mail address is required", validate.ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	creds := map[string]string{}
	known := map[string]bool{}
	for _, f := range info.Fields {
		known[f.Key] = true
		v := strings.TrimSpace(cfg.Credentials[f.Key])
		if len(v) > 4096 {
			return fmt.Errorf("%w: %s is too long", validate.ErrInvalid, f.Label)
		}
		if v == "" && f.Secret && m.cfg.Provider == cfg.Provider {
			v = m.cfg.Credentials[f.Key]
		}
		if v == "" && !f.Optional {
			return fmt.Errorf("%w: %s is required for %s", validate.ErrInvalid, f.Label, info.Name)
		}
		if v != "" {
			creds[f.Key] = v
		}
	}
	for k := range cfg.Credentials {
		if !known[k] {
			return fmt.Errorf("%w: %s does not take %q", validate.ErrInvalid, info.Name, k)
		}
	}
	cfg.Credentials = creds
	if err := m.saveLocked(cfg); err != nil {
		return err
	}
	m.lastError = ""
	m.kick()
	return nil
}

// Clear removes the configuration and the certificate obtained through it.
func (m *Manager) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Domain != "" {
		if info := m.certs.Info(); info.Custom != nil && covers(info.Custom.DNSNames, m.cfg.Domain) {
			if err := m.certs.SetCustom(nil, nil); err != nil {
				return err
			}
		}
	}
	m.cfg = Config{}
	m.lastError, m.lastAttempt, m.lastSuccess = "", nil, nil
	if err := os.Remove(filepath.Join(m.dir, configFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) saveLocked(cfg Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.dir, configFile), raw, 0o600); err != nil {
		return fmt.Errorf("write acme configuration: %w", err)
	}
	m.cfg = cfg
	return nil
}

// kick wakes the background loop.
func (m *Manager) kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Trigger requests an immediate issuance/renewal attempt.
func (m *Manager) Trigger() { m.kick() }

// NeedsIssue reports whether a certificate should be requested now.
func (m *Manager) NeedsIssue() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.needsIssueLocked()
}

func (m *Manager) needsIssueLocked() bool {
	if m.cfg.Provider == "" {
		return false
	}
	info := m.certs.Info()
	if info.Custom == nil || !covers(info.Custom.DNSNames, m.cfg.Domain) {
		return true
	}
	return m.now().Add(renewBefore).After(info.Custom.NotAfter)
}

// covers reports whether names include both the domain and its wildcard.
func covers(names []string, domain string) bool {
	var base, wild bool
	for _, n := range names {
		n = strings.ToLower(n)
		base = base || n == domain
		wild = wild || n == "*."+domain
	}
	return base && wild
}

// Run checks periodically (and on demand) whether the certificate must be obtained or
// renewed, with a short retry delay after failures.
func (m *Manager) Run(ctx context.Context) {
	retry := time.Hour
	for {
		if m.NeedsIssue() {
			if err := m.Issue(ctx); err != nil {
				m.log.Warn("acme issuance failed", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retry):
					continue
				case <-m.wake:
					continue
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
		case <-m.wake:
		}
	}
}

// Issue obtains a certificate for the configured domain (and its wildcard) now.
func (m *Manager) Issue(ctx context.Context) (err error) {
	m.mu.Lock()
	if m.issuing {
		m.mu.Unlock()
		return errors.New("issuance already in progress")
	}
	cfg := m.cfg
	if cfg.Provider == "" {
		m.mu.Unlock()
		return errors.New("acme is not configured")
	}
	m.issuing = true
	ts := m.now()
	m.lastAttempt = &ts
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.issuing = false
		if err != nil {
			m.lastError = err.Error()
		} else {
			m.lastError = ""
			ok := m.now()
			m.lastSuccess = &ok
		}
		n := m.notifier
		m.mu.Unlock()
		if n == nil {
			return
		}
		if err != nil {
			n.Notify(ctx, notify.Event{Kind: "acme.failed", Level: notify.Error, Title: "Certificate for *." + cfg.Domain + " not renewed", Message: err.Error() + "\nEnvoryx retries automatically; the current certificate stays valid until it expires."})
		} else {
			n.Clear("acme.failed|")
			n.Notify(ctx, notify.Event{Kind: "acme.renewed", Level: notify.Info, Title: "Certificate for *." + cfg.Domain + " issued", Message: "The Let's Encrypt certificate was obtained/renewed successfully."})
		}
	}()

	propagation := 5 * time.Minute
	if p, ok := providerInfo(cfg.Provider); ok && p.PropagationMinutes > 0 {
		propagation = time.Duration(p.PropagationMinutes) * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, propagation+issueSlack)
	defer cancel()
	provider, err := newProvider(cfg.Provider, cfg.Credentials, m.httpClient, m.providerBase)
	if err != nil {
		return err
	}
	accountKey, err := m.accountKey()
	if err != nil {
		return err
	}
	dir := m.directoryURL
	if dir == "" {
		dir = acme.LetsEncryptURL
		if cfg.Staging {
			dir = stagingURL
		}
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: dir, HTTPClient: m.httpClient, UserAgent: "Envoryx"}
	if _, err := client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + cfg.Email}}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("acme register: %w", err)
	}
	names := []string{cfg.Domain, "*." + cfg.Domain}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return fmt.Errorf("acme order: %w", err)
	}

	// Present every dns-01 challenge first (both names share one TXT record name), then
	// wait for propagation and let the CA validate.
	type pending struct {
		authz *acme.Authorization
		chal  *acme.Challenge
		fqdn  string
		value string
	}
	var todo []pending
	var handles []string
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, h := range handles {
			if err := provider.Cleanup(cleanupCtx, h); err != nil {
				m.log.Warn("acme: cleanup of challenge record failed", "err", err)
			}
		}
	}()
	for _, u := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return fmt.Errorf("acme authorization: %w", err)
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range z.Challenges {
			if c.Type == "dns-01" {
				chal = c
			}
		}
		if chal == nil {
			return fmt.Errorf("acme: no dns-01 challenge offered for %s", z.Identifier.Value)
		}
		value, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return err
		}
		// Wildcard authorizations carry the bare name plus Wildcard=true (RFC 8555 §7.1.4).
		fqdn := "_acme-challenge." + strings.TrimPrefix(z.Identifier.Value, "*.")
		h, err := provider.Present(ctx, fqdn, value)
		if err != nil {
			return err
		}
		handles = append(handles, h)
		todo = append(todo, pending{authz: z, chal: chal, fqdn: fqdn, value: value})
	}
	for _, p := range todo {
		if err := m.waitPropagation(ctx, p.fqdn, p.value, propagation); err != nil {
			return err
		}
	}
	for _, p := range todo {
		if _, err := client.Accept(ctx, p.chal); err != nil {
			return fmt.Errorf("acme accept challenge: %w", err)
		}
	}
	for _, p := range todo {
		if _, err := client.WaitAuthorization(ctx, p.authz.URI); err != nil {
			return fmt.Errorf("acme validation for %s failed: %w", p.authz.Identifier.Value, err)
		}
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: names}, certKey)
	if err != nil {
		return err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("acme finalize: %w", err)
	}
	var certPEM []byte
	for _, b := range der {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := m.certs.SetCustom(certPEM, keyPEM); err != nil {
		return fmt.Errorf("store certificate: %w", err)
	}
	m.log.Info("acme certificate obtained", "domain", cfg.Domain, "staging", cfg.Staging)
	return nil
}

// waitPropagation polls until the TXT record is visible at every name server of the zone.
func (m *Manager) waitPropagation(ctx context.Context, fqdn, value string, max time.Duration) error {
	deadline := m.now().Add(max)
	for {
		txts, err := m.lookupTXT(ctx, fqdn)
		if err == nil {
			for _, t := range txts {
				if t == value {
					return nil
				}
			}
		}
		if m.now().After(deadline) {
			return fmt.Errorf("acme: TXT record %s did not become visible at the zone's name servers within %s", fqdn, max)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// accountKey loads or creates the ACME account key.
func (m *Manager) accountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(m.dir, accountKeyFile)
	raw, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("acme account key is not PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
