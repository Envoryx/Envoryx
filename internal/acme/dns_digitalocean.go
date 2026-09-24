package acme

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const digitalOceanAPI = "https://api.digitalocean.com/v2"

type digitalOcean struct {
	token string
	http  *http.Client
	base  string
}

func (d *digitalOcean) do(ctx context.Context, method, path string, body, out any) (int, error) {
	status, raw, err := jsonRequest(ctx, d.http, method, d.base+path, body, map[string]string{"Authorization": "Bearer " + d.token})
	if err != nil {
		return 0, fmt.Errorf("digitalocean: %w", err)
	}
	if status/100 != 2 {
		var e struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d: %.200s", status, raw)
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return status, fmt.Errorf("digitalocean: token rejected (%s); it needs the domain scopes", msg)
		}
		return status, fmt.Errorf("digitalocean: %s", msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, fmt.Errorf("digitalocean: unexpected response: %.200s", raw)
		}
	}
	return status, nil
}

// Present implements DNSProvider.
func (d *digitalOcean) Present(ctx context.Context, fqdn, value string) (string, error) {
	zone := ""
	for _, name := range zoneCandidates(fqdn) {
		status, err := d.do(ctx, http.MethodGet, "/domains/"+url.PathEscape(name), nil, nil)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return "", err
		}
		zone = name
		break
	}
	if zone == "" {
		return "", fmt.Errorf("digitalocean: no domain found for %s on this account", fqdn)
	}
	var res struct {
		Record struct {
			ID int64 `json:"id"`
		} `json:"domain_record"`
	}
	body := map[string]any{"type": "TXT", "name": relativeName(fqdn, zone), "data": value, "ttl": 30}
	if _, err := d.do(ctx, http.MethodPost, "/domains/"+url.PathEscape(zone)+"/records", body, &res); err != nil {
		return "", err
	}
	return zone + "/" + strconv.FormatInt(res.Record.ID, 10), nil
}

// Cleanup implements DNSProvider.
func (d *digitalOcean) Cleanup(ctx context.Context, handle string) error {
	zone, id, ok := strings.Cut(handle, "/")
	if !ok {
		return errors.New("digitalocean: bad record handle")
	}
	status, err := d.do(ctx, http.MethodDelete, "/domains/"+url.PathEscape(zone)+"/records/"+id, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
