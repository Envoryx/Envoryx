package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceBackupLifecycle(t *testing.T) {
	a := newApp(t)
	if r := a.do(http.MethodGet, "/api/v1/instance/backups", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", r.status)
	}
	a.setupAndLogin()

	r := a.do(http.MethodGet, "/api/v1/instance/backups", nil, false)
	if r.status != http.StatusOK || len(r.body["backups"].([]any)) != 0 || r.body["pendingRestore"] != nil || r.body["canRestart"] != true {
		t.Fatalf("empty list = %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/instance/backups", map[string]string{"note": "first"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	backup := r.body["backup"].(map[string]any)
	id := backup["id"].(string)
	if backup["kind"] != "manual" || backup["meta"].(map[string]any)["note"] != "first" {
		t.Fatalf("backup = %v", backup)
	}

	// Download is the archive itself, importable again as an upload.
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/instance/backups/"+id+"/download", nil)
	req.AddCookie(a.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	archive, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/gzip" || len(archive) == 0 {
		t.Fatalf("download = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("file", "envoryx.tar.gz")
	_, _ = fw.Write(archive)
	_ = mw.Close()
	upload := func(withCSRF bool) resp {
		req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/instance/backups/upload", bytes.NewReader(form.Bytes()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if withCSRF {
			req.Header.Set("X-Requested-With", "Envoryx")
		}
		req.AddCookie(a.cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		out := resp{status: res.StatusCode, raw: raw}
		_ = json.Unmarshal(raw, &out.body)
		return out
	}
	if r := upload(false); r.status != http.StatusForbidden {
		t.Fatalf("upload without CSRF header = %d", r.status)
	}
	r = upload(true)
	if r.status != http.StatusCreated || r.body["backup"].(map[string]any)["kind"] != "upload" {
		t.Fatalf("upload = %d %s", r.status, r.raw)
	}
	uploaded := r.body["backup"].(map[string]any)["id"].(string)

	// Restore needs the confirmation word, then schedules and asks for a restart.
	r = a.do(http.MethodPost, "/api/v1/instance/backups/"+id+"/restore", map[string]string{"confirm": "nope"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("restore without confirm = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/instance/backups/"+id+"/restore", map[string]string{"confirm": "restore"}, true)
	if r.status != http.StatusAccepted || r.body["restarting"] != true || a.restarts != 1 {
		t.Fatalf("restore = %d %s (restarts %d)", r.status, r.raw, a.restarts)
	}
	if _, err := os.Stat(filepath.Join(a.cfgDir, ".restore-pending")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	r = a.do(http.MethodGet, "/api/v1/instance/backups", nil, false)
	if r.body["pendingRestore"].(map[string]any)["id"] != id {
		t.Fatalf("pending = %s", r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/instance/backups/"+id, nil, true); r.status != http.StatusConflict {
		t.Fatalf("delete scheduled = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/instance/restore", nil, true); r.status != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", r.status, r.raw)
	}
	for _, del := range []string{id, uploaded} {
		if r := a.do(http.MethodDelete, "/api/v1/instance/backups/"+del, nil, true); r.status != http.StatusNoContent {
			t.Fatalf("delete %s = %d %s", del, r.status, r.raw)
		}
	}
	if r := a.do(http.MethodDelete, "/api/v1/instance/backups/manual-00000000-000000-0000", nil, true); r.status != http.StatusNotFound {
		t.Fatalf("delete unknown = %d", r.status)
	}
	if r := a.do(http.MethodGet, "/api/v1/instance/backups/..%2F..%2Fetc%2Fpasswd/download", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("traversal = %d", r.status)
	}
}
