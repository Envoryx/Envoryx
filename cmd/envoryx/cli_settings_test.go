package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func settingsRoutes() map[string]func(http.ResponseWriter, *http.Request) {
	answer := map[string]any{
		"publicHost": "192.168.1.5", "baseDomain": "test", "forceHttps": false, "lockedSettings": []string{"baseDomain"},
		"logHistory": map[string]any{"enabled": true, "retentionDays": 14, "maxMb": 1024}, "metrics": map[string]any{"retentionDays": 30},
		"sshAuthorizedKeys": "ssh-ed25519 AAA a\nssh-ed25519 BBB b",
	}
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/settings":   func(w http.ResponseWriter, r *http.Request) { writeJSON(w, answer) },
		"PATCH /api/v1/settings": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, answer) },
		"GET /api/v1/projects/{id}/domains": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"domains": []map[string]any{
				{"hostname": "acme-shop.test", "default": true},
				{"id": "55555555-5555-4555-8555-555555555555", "hostname": "shop.example.com"},
			}})
		},
		"POST /api/v1/projects/{id}/domains": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"domain": map[string]any{"hostname": "www.example.com"}})
		},
		"DELETE /api/v1/projects/{id}/domains/{domain}": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	}
}

func TestSettingsShow(t *testing.T) {
	srv := newFakeServer(t, settingsRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"settings"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"public-host", "192.168.1.5", "fixed by the hoster", "force-https", "off", "log-history-days", "14", "metrics-days", "30"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	// The instance's SSH keys are the web interface's business only.
	if strings.Contains(out.String(), "AAA") || strings.Contains(out.String(), "ssh-keys") {
		t.Fatal("the instance's SSH keys are shown")
	}
}

// Every NAME=VALUE becomes one field of one PATCH, nested where the API nests it.
func TestSettingsSetBuildsOnePatch(t *testing.T) {
	srv := newFakeServer(t, settingsRoutes())
	c, _, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"settings", "set", "force-https=on", "public-host=", "log-history-days=7", "log-history=off", "metrics-days=60", "xdebug-client-host=@laptop"})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies["/api/v1/settings"], &sent); err != nil {
		t.Fatal(err)
	}
	lh, _ := sent["logHistory"].(map[string]any)
	if sent["forceHttps"] != true || sent["publicHost"] != "" || sent["metricsRetentionDays"] != float64(60) ||
		lh["retentionDays"] != float64(7) || lh["enabled"] != false || sent["xdebugClientHost"] != "@laptop" || sent["metrics"] != nil {
		t.Fatalf("sent %v", sent)
	}
	// The instance's SSH keys are set in the web interface only (the API refuses tokens).
	for _, bad := range [][]string{{"settings", "set", "ssh-keys=ssh-ed25519 CCC c"}, {"settings", "set", "nope=1"}, {"settings", "set", "force-https=maybe"}, {"settings", "set", "metrics-days=x"}, {"settings", "set", "force-https"}} {
		if err := c.run(context.Background(), bad); !errors.Is(err, errCLIUsage) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
}

func TestDomains(t *testing.T) {
	srv := newFakeServer(t, settingsRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"domain", "list", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "shop.example.com") || !strings.Contains(out.String(), "default") {
		t.Fatalf("output: %s", out.String())
	}
	if err := c.run(context.Background(), []string{"domain", "add", "acme-shop", "www.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.run(context.Background(), []string{"domain", "remove", "acme-shop", "SHOP.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(srv.requests, "\n"), "DELETE "+shopPath+"/domains/55555555-5555-4555-8555-555555555555") {
		t.Fatalf("requests: %v", srv.requests)
	}
	if err := c.run(context.Background(), []string{"domain", "remove", "acme-shop", "acme-shop.test"}); err == nil || !strings.Contains(err.Error(), "can't be removed") {
		t.Fatalf("default domain: %v", err)
	}
}
