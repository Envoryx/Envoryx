package acme

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Porkbun's API takes the keys in every JSON body and answers {"status": "SUCCESS"} or
// {"status": "ERROR", "message": …}. API access must be switched on per domain.

const porkbunAPI = "https://api.porkbun.com/api/json/v3"

type porkbun struct {
	apiKey string
	secret string
	http   *http.Client
	base   string
}

func (p *porkbun) do(ctx context.Context, path string, body map[string]any) (json.RawMessage, error) {
	if body == nil {
		body = map[string]any{}
	}
	body["apikey"], body["secretapikey"] = p.apiKey, p.secret
	status, raw, err := jsonRequest(ctx, p.http, http.MethodPost, p.base+path, body, nil)
	if err != nil {
		return nil, fmt.Errorf("porkbun: %w", err)
	}
	var res struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("porkbun: unexpected response (HTTP %d): %.200s", status, raw)
	}
	if !strings.EqualFold(res.Status, "SUCCESS") {
		if res.Message == "" {
			res.Message = fmt.Sprintf("HTTP %d", status)
		}
		return nil, fmt.Errorf("porkbun: %s", res.Message)
	}
	return raw, nil
}

// Present implements DNSProvider.
func (p *porkbun) Present(ctx context.Context, fqdn, value string) (string, error) {
	zone := ""
	var lastErr error
	for _, name := range zoneCandidates(fqdn) {
		if _, err := p.do(ctx, "/dns/retrieve/"+url.PathEscape(name), nil); err != nil {
			lastErr = err
			if strings.Contains(strings.ToLower(err.Error()), "api key") {
				return "", err // wrong keys: no point in trying the other names
			}
			continue
		}
		zone = name
		break
	}
	if zone == "" {
		return "", fmt.Errorf("porkbun: no domain found for %s (is API access switched on for it?): %v", fqdn, lastErr)
	}
	raw, err := p.do(ctx, "/dns/create/"+url.PathEscape(zone), map[string]any{"name": relativeName(fqdn, zone), "type": "TXT", "content": value, "ttl": "600"})
	if err != nil {
		return "", err
	}
	var res struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.ID == "" {
		return "", fmt.Errorf("porkbun: no record id in the answer")
	}
	return zone + "/" + res.ID.String(), nil
}

// Cleanup implements DNSProvider.
func (p *porkbun) Cleanup(ctx context.Context, handle string) error {
	zone, id, ok := strings.Cut(handle, "/")
	if !ok {
		return errors.New("porkbun: bad record handle")
	}
	_, err := p.do(ctx, "/dns/delete/"+url.PathEscape(zone)+"/"+url.PathEscape(id), nil)
	return err
}
