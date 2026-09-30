package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// The project responses carry no service credentials for anyone: the object storage
// keys, the RabbitMQ password and the search engines' keys come only from the
// operate-scoped credentials endpoints.
func TestProjectResponsesCarryNoServiceSecrets(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	admin := a.cookie
	create := map[string]any{"name": "Shop", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"},
		"storage": map[string]any{}, "rabbitmq": map[string]any{}, "meilisearch": map[string]any{}, "typesense": map[string]any{}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	secret := func(path, obj, field string) string {
		t.Helper()
		r := a.do(http.MethodGet, "/api/v1/projects/"+id+path, nil, false)
		v, _ := r.body[obj].(map[string]any)[field].(string)
		if r.status != http.StatusOK || v == "" {
			t.Fatalf("%s: %d %s", path, r.status, r.raw)
		}
		return v
	}
	secrets := map[string]string{
		"storage access key": secret("/storage/credentials", "storage", "accessKey"),
		"storage secret key": secret("/storage/credentials", "storage", "secretKey"),
		"rabbitmq password":  secret("/rabbitmq/credentials", "credentials", "password"),
		"meilisearch key":    secret("/meilisearch/credentials", "credentials", "apiKey"),
		"typesense key":      secret("/typesense/credentials", "credentials", "apiKey"),
	}
	check := func(who string) {
		t.Helper()
		for _, path := range []string{"/api/v1/projects", "/api/v1/projects/" + id, "/api/v1/dashboard"} {
			r := a.do(http.MethodGet, path, nil, false)
			if r.status != http.StatusOK {
				t.Fatalf("%s %s: %d %s", who, path, r.status, r.raw)
			}
			for name, v := range secrets {
				if strings.Contains(string(r.raw), v) {
					t.Errorf("%s gets the %s from %s", who, name, path)
				}
			}
		}
	}
	check("admin")
	// What is not secret stays.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	if !strings.Contains(string(r.raw), `"bucket":"shop"`) || !strings.Contains(string(r.raw), `"username":"envoryx"`) {
		t.Fatalf("non-secret settings missing: %s", r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "vera", "role": "viewer"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.status, r.raw)
	}
	invite := r.body["inviteUrl"].(string)
	a.cookie = nil
	r = a.do(http.MethodPost, "/api/v1/invites/"+invite[strings.LastIndex(invite, "/")+1:], map[string]string{"password": "vera-secret-123"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("accept: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie
	check("viewer")
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/storage/credentials", nil, false); r.status != http.StatusForbidden {
		t.Fatalf("viewer on the storage credentials: %d %s", r.status, r.raw)
	}
	a.cookie = admin
}
