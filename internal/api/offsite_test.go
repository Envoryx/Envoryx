package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/runtime"
)

// memTarget stands in for a bucket.
type memTarget struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memTarget) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	b, err := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[key] = b
	return int64(len(b)), err
}

func (m *memTarget) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	if !ok {
		return nil, offsite.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memTarget) List(_ context.Context, dir string) ([]offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []offsite.Object
	for k, b := range m.files {
		if rest, ok := strings.CutPrefix(k, dir+"/"); ok && !strings.Contains(rest, "/") {
			out = append(out, offsite.Object{Key: k, Size: int64(len(b))})
		}
	}
	return out, nil
}

func (m *memTarget) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)
	return nil
}

func (m *memTarget) Close() error { return nil }

func TestOffsiteEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	ctx := context.Background()

	r := a.do(http.MethodPost, "/api/v1/offsite/targets", map[string]any{
		"name": "B2", "type": "s3", "enabled": true, "auto": true, "endpoint": "https://s3.eu-central-003.backblazeb2.com",
		"region": "eu-central-003", "bucket": "acme-backups", "accessKey": "key-id", "secretKey": "very-secret-key",
		"encrypt": true, "passphrase": "correct horse battery staple",
	}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create target: %d %s", r.status, r.raw)
	}
	if strings.Contains(string(r.raw), "very-secret-key") || strings.Contains(string(r.raw), "correct horse") {
		t.Fatalf("secrets returned: %s", r.raw)
	}
	target := r.body["target"].(map[string]any)
	targetID := target["id"].(string)
	if target["secrets"].(map[string]any)["secretKey"] != true || target["prefix"] != "envoryx" {
		t.Fatalf("target: %s", r.raw)
	}
	// An update without secrets keeps them.
	r = a.do(http.MethodPut, "/api/v1/offsite/targets/"+targetID, map[string]any{
		"name": "Backblaze", "type": "s3", "enabled": true, "auto": true, "endpoint": "https://s3.eu-central-003.backblazeb2.com",
		"region": "eu-central-003", "bucket": "acme-backups", "accessKey": "key-id", "encrypt": true, "keep": 7,
	}, true)
	if r.status != http.StatusOK || r.body["target"].(map[string]any)["secrets"].(map[string]any)["passphrase"] != true {
		t.Fatalf("update: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/offsite/test", map[string]any{"id": targetID, "name": "Backblaze", "type": "s3", "endpoint": "https://s3.eu-central-003.backblazeb2.com", "region": "eu-central-003", "bucket": "acme-backups", "accessKey": "key-id", "encrypt": true}, true)
	if r.status != http.StatusOK {
		t.Fatalf("test: %d %s", r.status, r.raw)
	}

	// A project backup with "offsite" goes up; the list shows the copy.
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create project: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups", map[string]any{"files": true, "offsite": true}, true)
	if r.status != http.StatusCreated || r.body["offsite"] == nil {
		t.Fatalf("backup: %d %s", r.status, r.raw)
	}
	backupID := r.body["backup"].(map[string]any)["id"].(string)
	a.offsite.Pass(ctx)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/backups", nil, false)
	copies := r.body["offsite"].(map[string]any)[backupID].([]any)
	if len(copies) != 1 || copies[0].(map[string]any)["status"] != "done" || copies[0].(map[string]any)["targetName"] != "Backblaze" {
		t.Fatalf("copies: %s", r.raw)
	}
	if names := r.body["offsiteTargets"].([]any); len(names) != 1 || strings.Contains(string(r.raw), "acme-backups") {
		t.Fatalf("the backup list names the targets and nothing more: %s", r.raw)
	}

	// Gone locally, fetched back from the target.
	if r := a.do(http.MethodDelete, "/api/v1/projects/"+id+"/backups/"+backupID, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/offsite/"+targetID, nil, false)
	remote := r.body["backups"].([]any)
	if r.status != http.StatusOK || len(remote) != 1 || remote[0].(map[string]any)["encrypted"] != true {
		t.Fatalf("remote list: %d %s", r.status, r.raw)
	}
	key := remote[0].(map[string]any)["key"].(string)
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/offsite/"+targetID+"/fetch", map[string]any{"key": key}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("fetch: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/backups", nil, false)
	if list := r.body["backups"].([]any); len(list) != 1 || list[0].(map[string]any)["missing"] != false {
		t.Fatalf("after fetch: %s", r.raw)
	}

	// Instance backups: upload by hand, list and fetch from the target.
	r = a.do(http.MethodPost, "/api/v1/instance/backups", map[string]any{"note": "before"}, true)
	instID := r.body["backup"].(map[string]any)["id"].(string)
	if r := a.do(http.MethodPost, "/api/v1/instance/backups/"+instID+"/offsite", map[string]any{"targets": []string{targetID}}, true); r.status != http.StatusAccepted {
		t.Fatalf("instance upload: %d %s", r.status, r.raw)
	}
	a.offsite.Pass(ctx)
	r = a.do(http.MethodGet, "/api/v1/offsite/targets/"+targetID+"/instance", nil, false)
	if list := r.body["backups"].([]any); len(list) != 1 {
		t.Fatalf("remote instance backups: %s", r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/offsite/targets/"+targetID+"/instance/fetch", map[string]any{"key": "instance/" + instID + ".tar.gz.age"}, true)
	if r.status != http.StatusCreated || r.body["backup"].(map[string]any)["kind"] != "upload" {
		t.Fatalf("instance fetch: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/offsite", nil, false)
	if last := r.body["targets"].([]any)[0].(map[string]any)["last"]; last == nil {
		t.Fatalf("overview: %s", r.raw)
	}

	// Keys outside the layout are refused; removing a target keeps its copies.
	if r := a.do(http.MethodPost, "/api/v1/offsite/targets/"+targetID+"/remove", map[string]any{"key": "../../etc/passwd"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("foreign key: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/offsite/targets/"+targetID, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete target: %d %s", r.status, r.raw)
	}
	if len(a.offsiteMem.files) != 2 {
		t.Fatalf("copies on the target: %d", len(a.offsiteMem.files))
	}
}
