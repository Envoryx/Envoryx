package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
)

func TestSiteImport(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	upload := func(site []byte, dump string) resp {
		t.Helper()
		var form bytes.Buffer
		mw := multipart.NewWriter(&form)
		fw, _ := mw.CreateFormFile("site", "site.zip")
		_, _ = fw.Write(site)
		if dump != "" {
			dw, _ := mw.CreateFormFile("database", "dump.sql")
			_, _ = dw.Write([]byte(dump))
		}
		_ = mw.Close()
		req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/site-imports", &form)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-Requested-With", "Envoryx")
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

	if r := upload([]byte("not an archive"), ""); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("upload of a text file = %d %s", r.status, r.raw)
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("www/index.html")
	_, _ = w.Write([]byte("<h1>old site</h1>"))
	_ = zw.Close()

	r := upload(zbuf.Bytes(), "")
	if r.status != http.StatusCreated {
		t.Fatalf("upload = %d %s", r.status, r.raw)
	}
	imp := r.body["import"].(map[string]any)
	id := imp["id"].(string)
	analysis := imp["analysis"].(map[string]any)
	if analysis["runtime"] != "static" || analysis["root"] != "www/" {
		t.Fatalf("analysis = %v", analysis)
	}
	if r := a.do(http.MethodGet, "/api/v1/site-imports/"+id, nil, false); r.status != http.StatusOK {
		t.Fatalf("get = %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Old Site", "import": map[string]any{"id": id, "adaptConfig": true}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	if res := r.body["import"].(map[string]any); res["framework"].(map[string]any)["id"] != "static" {
		t.Fatalf("import result = %v", res)
	}
	if r := a.do(http.MethodGet, "/api/v1/site-imports/"+id, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("the upload must be used up, get = %d", r.status)
	}

	// A discarded upload is gone.
	r = upload(zbuf.Bytes(), "")
	id = r.body["import"].(map[string]any)["id"].(string)
	if r := a.do(http.MethodDelete, "/api/v1/site-imports/"+id, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/site-imports/"+id, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("get after delete = %d", r.status)
	}
}
