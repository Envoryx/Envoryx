package project

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

// With the local CA in use, the containers that run project code mount the trust directory
// and point the runtimes at it; the web server doesn't need it, and PHP gets it in its ini.
func TestCATrustReachesTheRuntimeContainers(t *testing.T) {
	e := newEnv(t)
	e.m.SetCATrust(true)
	ctx := context.Background()
	req := phpRequest("Trusted", true)
	req.Env = append(req.Env, EnvVarRequest{Key: "CURL_CA_BUNDLE", Value: "/app/company.pem"})
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	mount := docker.MountSpec{Type: "bind", Source: "/host/appdata/envoryx/ca-trust", Target: caTrustTarget, ReadOnly: true}
	php, _ := e.engine.Container("envoryx-trusted-php")
	if !slices.Contains(php.Spec.Mounts, mount) {
		t.Errorf("php mounts: %+v", php.Spec.Mounts)
	}
	for _, kv := range []string{"SSL_CERT_FILE=/etc/envoryx/ca/ca-bundle.crt", "REQUESTS_CA_BUNDLE=/etc/envoryx/ca/ca-bundle.crt", "NODE_EXTRA_CA_CERTS=/etc/envoryx/ca/envoryx-ca.crt", "CURL_CA_BUNDLE=/app/company.pem"} {
		if !slices.Contains(php.Spec.Env, kv) {
			t.Errorf("php env lacks %s: %v", kv, php.Spec.Env)
		}
	}
	if strings.Contains(strings.Join(php.Spec.Env, "\n"), "CURL_CA_BUNDLE=/etc/envoryx") {
		t.Errorf("the project's own CURL_CA_BUNDLE must win: %v", php.Spec.Env)
	}
	web, _ := e.engine.Container("envoryx-trusted-web")
	if slices.Contains(web.Spec.Mounts, mount) || strings.Contains(strings.Join(web.Spec.Env, "\n"), "SSL_CERT_FILE") {
		t.Errorf("web server got the trust files: %+v", web.Spec)
	}
	ini, err := os.ReadFile(filepath.Join(e.cfgDir, "projects", view.Project.ID, "php", "zz-envoryx.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ini), `openssl.cafile = "/etc/envoryx/ca/ca-bundle.crt"`) || !strings.Contains(string(ini), `curl.cainfo = "/etc/envoryx/ca/ca-bundle.crt"`) {
		t.Errorf("php.ini:\n%s", ini)
	}
}

// Without the local CA (no HTTPS proxy) nothing changes.
func TestNoCATrustWithoutTheLocalCA(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Plain", true))
	if err != nil {
		t.Fatal(err)
	}
	php, _ := e.engine.Container("envoryx-plain-php")
	for _, m := range php.Spec.Mounts {
		if m.Target == caTrustTarget {
			t.Errorf("trust mounted: %+v", php.Spec.Mounts)
		}
	}
	if strings.Contains(strings.Join(php.Spec.Env, "\n"), "SSL_CERT_FILE") {
		t.Errorf("env: %v", php.Spec.Env)
	}
	ini, _ := os.ReadFile(filepath.Join(e.cfgDir, "projects", view.Project.ID, "php", "zz-envoryx.ini"))
	if strings.Contains(string(ini), "cafile") {
		t.Errorf("php.ini:\n%s", ini)
	}
}
