//go:build integration

package project

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/tlsca"
)

// TestIntegrationCATrust serves HTTPS with a certificate of the local CA inside a project
// container and fetches it from there: the runtime trusts the CA through the mounted
// trust directory, as an application calling its own https://<slug>.test does.
func TestIntegrationCATrust(t *testing.T) {
	m := integrationManager(t)
	ctx := context.Background()
	paths, err := m.paths()
	if err != nil {
		t.Fatal(err)
	}
	certs, err := tlsca.Open(filepath.Join(t.TempDir(), "ca"))
	if err != nil {
		t.Fatal(err)
	}
	if err := certs.WriteTrust(filepath.Join(paths.ConfigDir, CATrustDir)); err != nil {
		t.Skipf("no system bundle on this host: %v", err)
	}
	m.SetCATrust(true)

	view, err := m.Create(ctx, CreateRequest{Name: "Envoryx Integration Trust", CreateStarter: true, Start: true, Node: &NodeRequest{}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, slug := view.Project.ID, view.Project.Slug
	t.Cleanup(func() {
		if err := m.Delete(context.Background(), id, DeleteOptions{Confirm: slug, DeleteFiles: true}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	leaf, err := certs.Certificate("localhost")
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(leaf.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.ProjectsDir, slug)
	_ = os.WriteFile(filepath.Join(dir, "leaf.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "leaf.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o644)

	c, err := m.ServiceContainer(ctx, id, store.ServiceNode)
	if err != nil {
		t.Fatal(err)
	}
	script := `const fs = require("fs");
const srv = require("https").createServer({cert: fs.readFileSync("leaf.crt"), key: fs.readFileSync("leaf.key")}, (q, r) => r.end("trusted"));
srv.listen(8443, "127.0.0.1", () => fetch("https://localhost:8443/").then(r => r.text()).then(b => console.log(b), e => console.log("FAIL", e.cause ? e.cause.code : e)).finally(() => srv.close()));`
	res, err := m.engine.Exec(ctx, c.ID, []string{"node", "-e", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "trusted" {
		t.Fatalf("node did not trust the local CA: %q %q", res.Stdout, res.Stderr)
	}
	// The OpenSSL bundle is there for the others, readable by the project user.
	res, err = m.engine.Exec(ctx, c.ID, []string{"sh", "-c", `grep -c "BEGIN CERTIFICATE" "$SSL_CERT_FILE"`}, nil)
	if n, _ := strconv.Atoi(strings.TrimSpace(res.Stdout)); err != nil || n < 2 {
		t.Fatalf("bundle: %q %q %v", res.Stdout, res.Stderr, err)
	}
}
