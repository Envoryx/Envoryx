package api_test

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
)

func TestAuditFiltersExportAndRetention(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	cookie := a.cookie
	a.cookie = nil
	// A failed sign-in under a name a spreadsheet would run as a formula.
	a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "=HYPERLINK(1)", "password": "wrong-password"}, true)
	a.cookie = cookie

	r := a.do(http.MethodGet, "/api/v1/audit?action=auth.login_failed", nil, false)
	entries := r.body["entries"].([]any)
	if r.status != http.StatusOK || len(entries) != 1 || entries[0].(map[string]any)["username"] != "=HYPERLINK(1)" {
		t.Fatalf("filter by action: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/audit?limit=1", nil, false)
	if len(r.body["entries"].([]any)) != 1 || r.body["next"] == "" {
		t.Fatalf("a page of one has a next cursor: %s", r.raw)
	}
	next := r.body["next"].(string)
	r = a.do(http.MethodGet, "/api/v1/audit?limit=1&after="+next, nil, false)
	if r.status != http.StatusOK || len(r.body["entries"].([]any)) != 1 {
		t.Fatalf("second page: %s", r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/audit?since=yesterday", nil, false); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a date that is none: %d", r.status)
	}

	r = a.do(http.MethodGet, "/api/v1/audit/export?format=csv&action=auth.", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("csv export: %d %s", r.status, r.raw)
	}
	rows, err := csv.NewReader(bytes.NewReader(r.raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 || strings.Join(rows[0], ",") != "time,user,action,target_type,target_id,ip,details" {
		t.Fatalf("csv: %v", rows)
	}
	guarded := false
	for _, row := range rows[1:] {
		if !strings.HasPrefix(row[2], "auth.") {
			t.Fatalf("filtered export has %s", row[2])
		}
		if row[1] == "'=HYPERLINK(1)" {
			guarded = true
		}
	}
	if !guarded {
		t.Fatalf("a formula-like cell must be defused: %v", rows)
	}
	r = a.do(http.MethodGet, "/api/v1/audit/export?format=jsonl&action=auth.login_failed", nil, false)
	if lines := strings.Split(strings.TrimSpace(string(r.raw)), "\n"); r.status != http.StatusOK || len(lines) != 1 || !strings.Contains(lines[0], `"action":"auth.login_failed"`) {
		t.Fatalf("jsonl export: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/audit/export?format=xml", nil, false); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown format: %d", r.status)
	}

	r = a.do(http.MethodGet, "/api/v1/audit/users", nil, false)
	if users := r.body["users"].([]any); len(users) != 2 {
		t.Fatalf("users: %s", r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/audit/settings", nil, false)
	if r.body["settings"].(map[string]any)["retentionDays"] != float64(0) {
		t.Fatalf("default keeps everything: %s", r.raw)
	}
	if r := a.do(http.MethodPut, "/api/v1/audit/settings", map[string]int{"retentionDays": -1}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("negative retention: %d", r.status)
	}
	r = a.do(http.MethodPut, "/api/v1/audit/settings", map[string]int{"retentionDays": 90}, true)
	if r.status != http.StatusOK || r.body["settings"].(map[string]any)["retentionDays"] != float64(90) {
		t.Fatalf("set retention: %d %s", r.status, r.raw)
	}
}
