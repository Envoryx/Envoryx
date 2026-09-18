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

// Providers lists the supported DNS providers (key → display name).
var Providers = map[string]string{
	"cloudflare": "Cloudflare",
}

// newProvider instantiates a provider by key.
func newProvider(key, token string, httpClient *http.Client, baseURL string) (DNSProvider, error) {
	switch key {
	case "cloudflare":
		return &cloudflare{token: token, http: httpClient, base: baseURL}, nil
	}
	return nil, fmt.Errorf("unsupported DNS provider %q", key)
}

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
