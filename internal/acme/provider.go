package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DNSProvider creates and removes the TXT records of the ACME dns-01 challenge.
type DNSProvider interface {
	// Present creates a TXT record fqdn → value and returns an opaque handle for Cleanup.
	Present(ctx context.Context, fqdn, value string) (handle string, err error)
	// Cleanup removes the record created by Present.
	Cleanup(ctx context.Context, handle string) error
}

// Field is one credential a provider needs. Secret fields are never returned by the API;
// an update that leaves them empty keeps the stored value.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Optional bool   `json:"optional,omitempty"`
	// Hint says where to get it (English; the UI translates).
	Hint string `json:"hint,omitempty"`
}

// ProviderInfo describes a DNS provider for the UI.
type ProviderInfo struct {
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
	// PropagationMinutes is how long Envoryx waits for the record to show up at the
	// zone's name servers.
	PropagationMinutes int `json:"propagationMinutes"`
}

// ProviderList is every supported provider, in the order the UI offers them.
var ProviderList = []ProviderInfo{
	{Key: "cloudflare", Name: "Cloudflare", PropagationMinutes: 5, Fields: []Field{
		{Key: "token", Label: "API token", Secret: true, Hint: "My Profile → API Tokens → Create Token → “Edit zone DNS” template (Zone:Read + DNS:Edit for the zone)."},
	}},
	{Key: "hetzner", Name: "Hetzner", PropagationMinutes: 5, Fields: []Field{
		{Key: "token", Label: "API token", Secret: true, Hint: "Hetzner Console → the project holding the zone → Security → API tokens, with Read & Write. Tokens of the old DNS Console (dns.hetzner.com) no longer work."},
	}},
	{Key: "netcup", Name: "netcup", PropagationMinutes: 20, Fields: []Field{
		{Key: "customerNumber", Label: "Customer number"},
		{Key: "apiKey", Label: "API key", Secret: true, Hint: "Customer Control Panel → Master Data → API."},
		{Key: "apiPassword", Label: "API password", Secret: true},
	}},
	{Key: "route53", Name: "Amazon Route 53", PropagationMinutes: 5, Fields: []Field{
		{Key: "accessKeyId", Label: "Access key ID", Hint: "An IAM user allowed route53:ListHostedZonesByName, route53:GetHostedZone, route53:ListResourceRecordSets and route53:ChangeResourceRecordSets."},
		{Key: "secretAccessKey", Label: "Secret access key", Secret: true},
		{Key: "hostedZoneId", Label: "Hosted zone ID", Optional: true, Hint: "Only needed when the domain has several public hosted zones."},
	}},
	{Key: "digitalocean", Name: "DigitalOcean", PropagationMinutes: 5, Fields: []Field{
		{Key: "token", Label: "API token", Secret: true, Hint: "API → Tokens → Generate New Token with the domain scopes (read, create, delete)."},
	}},
	{Key: "porkbun", Name: "Porkbun", PropagationMinutes: 10, Fields: []Field{
		{Key: "apiKey", Label: "API key", Secret: true, Hint: "Account → API Access; also switch on API access for the domain."},
		{Key: "secretApiKey", Label: "Secret API key", Secret: true},
	}},
}

// Providers maps key → display name (kept for API clients that only need the names).
var Providers = func() map[string]string {
	out := map[string]string{}
	for _, p := range ProviderList {
		out[p.Key] = p.Name
	}
	return out
}()

func providerInfo(key string) (ProviderInfo, bool) {
	for _, p := range ProviderList {
		if p.Key == key {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// newProvider instantiates a provider by key. baseURL replaces the API address in tests.
func newProvider(key string, creds map[string]string, httpClient *http.Client, baseURL string) (DNSProvider, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	switch key {
	case "cloudflare":
		return &cloudflare{token: creds["token"], http: httpClient, base: baseURL}, nil
	case "hetzner":
		return &hetzner{token: creds["token"], http: httpClient, base: or(baseURL, hetznerAPI)}, nil
	case "digitalocean":
		return &digitalOcean{token: creds["token"], http: httpClient, base: or(baseURL, digitalOceanAPI)}, nil
	case "porkbun":
		return &porkbun{apiKey: creds["apiKey"], secret: creds["secretApiKey"], http: httpClient, base: or(baseURL, porkbunAPI)}, nil
	case "netcup":
		return &netcup{customer: creds["customerNumber"], apiKey: creds["apiKey"], password: creds["apiPassword"], http: httpClient, endpoint: or(baseURL, netcupAPI)}, nil
	case "route53":
		return &route53{accessKey: creds["accessKeyId"], secretKey: creds["secretAccessKey"], zoneID: creds["hostedZoneId"], http: httpClient, base: or(baseURL, route53API)}, nil
	}
	return nil, fmt.Errorf("unsupported DNS provider %q", key)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// zoneCandidates are the names a zone for fqdn may have, longest first:
// _acme-challenge.dev.example.com → dev.example.com, example.com.
func zoneCandidates(fqdn string) []string {
	labels := strings.Split(strings.Trim(strings.ToLower(fqdn), "."), ".")
	var out []string
	for i := 1; i < len(labels)-1; i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// relativeName is fqdn inside zone: _acme-challenge.dev in example.com.
func relativeName(fqdn, zone string) string {
	fqdn = strings.Trim(strings.ToLower(fqdn), ".")
	return strings.TrimSuffix(strings.TrimSuffix(fqdn, strings.ToLower(zone)), ".")
}

// readBody reads at most 1 MiB of an answer.
func readBody(r io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	return b
}

// jsonRequest sends a JSON request and returns status and body.
func jsonRequest(ctx context.Context, client *http.Client, method, u string, body any, header map[string]string) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	return res.StatusCode, readBody(res.Body), nil
}

// quoteTXT puts a TXT value in the quotes zone-file style APIs expect.
func quoteTXT(v string) string { return strconv.Quote(v) }

// ---- Cloudflare ---------------------------------------------------------------------

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// cloudflare talks to the Cloudflare v4 API with an API token that has Zone:Read and
// DNS:Edit for the zone.
type cloudflare struct {
	token string
	http  *http.Client
	base  string
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *cloudflare) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	base := c.base
	if base == "" {
		base = cloudflareAPI
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.http
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("cloudflare: unexpected response (%s): %.200s", res.Status, raw)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("cloudflare: token rejected (%s); it needs Zone:Read and Zone:DNS:Edit for the zone", strings.Join(msgs, "; "))
		}
		return fmt.Errorf("cloudflare: %s", strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// zoneID finds the zone that contains fqdn by trying ever shorter suffixes.
func (c *cloudflare) zoneID(ctx context.Context, fqdn string) (string, error) {
	labels := strings.Split(strings.Trim(fqdn, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		name := strings.Join(labels[i:], ".")
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := c.do(ctx, http.MethodGet, "/zones?per_page=50&name="+url.QueryEscape(name), nil, &zones); err != nil {
			return "", err
		}
		for _, z := range zones {
			if strings.EqualFold(z.Name, name) {
				return z.ID, nil
			}
		}
	}
	return "", fmt.Errorf("cloudflare: no zone found for %s (is the domain on this account and does the token allow Zone:Read?)", fqdn)
}

// Present implements DNSProvider.
func (c *cloudflare) Present(ctx context.Context, fqdn, value string) (string, error) {
	zone, err := c.zoneID(ctx, fqdn)
	if err != nil {
		return "", err
	}
	var rec struct {
		ID string `json:"id"`
	}
	body := map[string]any{"type": "TXT", "name": strings.TrimSuffix(fqdn, "."), "content": value, "ttl": 60, "comment": "Envoryx ACME challenge"}
	if err := c.do(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", body, &rec); err != nil {
		return "", err
	}
	return zone + "/" + rec.ID, nil
}

// Cleanup implements DNSProvider.
func (c *cloudflare) Cleanup(ctx context.Context, handle string) error {
	zone, id, ok := strings.Cut(handle, "/")
	if !ok {
		return errors.New("cloudflare: bad record handle")
	}
	return c.do(ctx, http.MethodDelete, "/zones/"+zone+"/dns_records/"+id, nil, nil)
}
