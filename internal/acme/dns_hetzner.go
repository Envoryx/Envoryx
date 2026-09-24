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
	"time"
)

// Hetzner DNS lives in the Hetzner Cloud API since the old DNS Console (dns.hetzner.com)
// and its API were shut down in May 2026. Records are grouped into RRSets (name + type);
// the add_records/remove_records actions change single values, so the challenges for the
// domain and its wildcard – two values on one name – do not overwrite each other.

const hetznerAPI = "https://api.hetzner.cloud/v1"

type hetzner struct {
	token string
	http  *http.Client
	base  string
}

type hetznerAction struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (h *hetzner) do(ctx context.Context, method, path string, body, out any) (int, error) {
	status, raw, err := jsonRequest(ctx, h.http, method, h.base+path, body, map[string]string{"Authorization": "Bearer " + h.token})
	if err != nil {
		return 0, fmt.Errorf("hetzner: %w", err)
	}
	if status/100 != 2 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d: %.200s", status, raw)
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return status, fmt.Errorf("hetzner: token rejected (%s); it needs Read & Write in the project that holds the zone", msg)
		}
		return status, fmt.Errorf("hetzner: %s", msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, fmt.Errorf("hetzner: unexpected response: %.200s", raw)
		}
	}
	return status, nil
}

func (h *hetzner) zone(ctx context.Context, fqdn string) (string, error) {
	for _, name := range zoneCandidates(fqdn) {
		status, err := h.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(name), nil, nil)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("hetzner: no zone found for %s in the project of this token", fqdn)
}

// wait follows an action until it is done.
func (h *hetzner) wait(ctx context.Context, a hetznerAction) error {
	for i := 0; a.Status == "running" && i < 60; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		var res struct {
			Action hetznerAction `json:"action"`
		}
		if _, err := h.do(ctx, http.MethodGet, "/actions/"+strconv.FormatInt(a.ID, 10), nil, &res); err != nil {
			return err
		}
		a = res.Action
	}
	if a.Status == "error" && a.Error != nil {
		return fmt.Errorf("hetzner: %s: %s", a.Error.Code, a.Error.Message)
	}
	return nil
}

func (h *hetzner) change(ctx context.Context, zone, name, action string, body map[string]any) error {
	var res struct {
		Action hetznerAction `json:"action"`
	}
	path := "/zones/" + url.PathEscape(zone) + "/rrsets/" + url.PathEscape(name) + "/TXT/actions/" + action
	if _, err := h.do(ctx, http.MethodPost, path, body, &res); err != nil {
		return err
	}
	return h.wait(ctx, res.Action)
}

// Present implements DNSProvider.
func (h *hetzner) Present(ctx context.Context, fqdn, value string) (string, error) {
	zone, err := h.zone(ctx, fqdn)
	if err != nil {
		return "", err
	}
	name := relativeName(fqdn, zone)
	record := map[string]any{"value": quoteTXT(value), "comment": "Envoryx ACME challenge"}
	if err := h.change(ctx, zone, name, "add_records", map[string]any{"ttl": 60, "records": []any{record}}); err != nil {
		return "", err
	}
	return strings.Join([]string{zone, name, value}, "\n"), nil
}

// Cleanup implements DNSProvider.
func (h *hetzner) Cleanup(ctx context.Context, handle string) error {
	parts := strings.SplitN(handle, "\n", 3)
	if len(parts) != 3 {
		return errors.New("hetzner: bad record handle")
	}
	return h.change(ctx, parts[0], parts[1], "remove_records", map[string]any{"records": []any{map[string]any{"value": quoteTXT(parts[2])}}})
}
