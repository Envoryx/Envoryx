package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestImportPacksTheFolderAndCreatesTheProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "old-blog")
	_ = os.MkdirAll(filepath.Join(dir, "wp-includes"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "wp-includes", "version.php"), []byte("<?php $wp_version = '6.6';"), 0o644)
	_ = os.Symlink("wp-config.php", filepath.Join(dir, "link.php"))
	dump := filepath.Join(t.TempDir(), "blog.sql")
	_ = os.WriteFile(dump, []byte("CREATE TABLE wp_options (x int);"), 0o644)

	var packed []string
	var dumpSent string
	var contentType string
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/site-imports": func(w http.ResponseWriter, r *http.Request) {
			contentType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"import": map[string]any{
				"id": "44444444-4444-4444-8444-444444444444", "siteName": "old-blog.tar.gz", "dumpName": "blog.sql",
				"analysis": map[string]any{
					"files": 3, "bytes": 100, "framework": map[string]any{"id": "wordpress", "name": "WordPress", "version": "6.6"},
					"runtime": "php", "phpVersion": "8.3", "docroot": "", "web": "apache", "database": "mariadb",
					"config":  map[string]any{"path": "wp-config.php", "mode": "adapt"},
					"notices": []map[string]any{{"level": "info", "text": "The archive's folder {{folder}} became the project directory.", "params": map[string]string{"folder": "x"}}},
				},
			}})
		},
		"POST /api/v1/projects": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{
				"project": map[string]any{"id": "55555555-5555-4555-8555-555555555555", "name": "old-blog", "slug": "old-blog", "hostnames": []string{"old-blog.test"}},
				"import":  map[string]any{"database": true, "adapted": map[string]any{"changed": []string{"wp-config.php"}, "originals": []string{"wp-config.envoryx-original.php"}}},
			})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"import", dir, "--db", dump, "--database", "mysql:8.4"}); err != nil {
		t.Fatal(err)
	}

	// The upload is a multipart form: the folder as a tarball, then the dump.
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(bytes.NewReader(srv.bodies["/api/v1/site-imports"]), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch part.FormName() {
		case "site":
			if part.FileName() != "old-blog.tar.gz" {
				t.Errorf("site file name %q", part.FileName())
			}
			gz, err := gzip.NewReader(part)
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gz)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				packed = append(packed, hdr.Name)
				if hdr.Name == "link.php" && (hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "wp-config.php") {
					t.Errorf("symlink packed as %+v", hdr)
				}
			}
		case "database":
			b, _ := io.ReadAll(part)
			dumpSent = string(b)
		}
	}
	slices.Sort(packed)
	if !slices.Equal(packed, []string{"link.php", "wp-config.php", "wp-includes/", "wp-includes/version.php"}) {
		t.Fatalf("packed %v", packed)
	}
	if dumpSent != "CREATE TABLE wp_options (x int);" {
		t.Fatalf("dump %q", dumpSent)
	}

	var sent createRequest
	if err := json.Unmarshal(srv.bodies["/api/v1/projects"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Name != "old-blog" || sent.Import == nil || sent.Import.ID != "44444444-4444-4444-8444-444444444444" || !sent.Import.AdaptConfig {
		t.Fatalf("create request: %+v", sent)
	}
	// The suggestion, with the flags winning.
	if sent.PHP == nil || sent.PHP.Version != "8.3" || sent.Database == nil || sent.Database.Type != "mysql" || sent.Database.Version != "8.4" || !strings.Contains(string(sent.Web), "apache") {
		t.Fatalf("create request: %+v", sent)
	}
	for _, want := range []string{"Recognised WordPress 6.6", "wp-config.php is wired to the project database", "note: The archive's folder x became", "Created old-blog", "original kept as wp-config.envoryx-original.php", "imported blog.sql"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
}

func TestImportDryRunDiscardsTheUpload(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "site.zip")
	_ = os.WriteFile(archive, []byte("PK\x03\x04"), 0o644)
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/site-imports": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"import": map[string]any{"id": "66666666-6666-4666-8666-666666666666", "siteName": "site.zip",
				"analysis": map[string]any{"files": 1, "framework": map[string]any{"id": "static", "name": "Static site"}, "runtime": "static", "notices": []any{}}}})
		},
		"DELETE /api/v1/site-imports/{id}": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"import", archive, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(srv.requests, "DELETE /api/v1/site-imports/66666666-6666-4666-8666-666666666666") || slices.Contains(srv.requests, "POST /api/v1/projects") {
		t.Fatalf("requests: %v", srv.requests)
	}
	if !strings.Contains(out.String(), "Dry run") {
		t.Fatalf("output: %s", out)
	}
}
