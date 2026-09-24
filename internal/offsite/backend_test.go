package offsite

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/envoryx/envoryx/internal/s3"
)

// exercise runs the same round trip against any backend.
func exercise(t *testing.T, b Backend, big []byte) {
	t.Helper()
	ctx := context.Background()
	if n, err := b.Put(ctx, "projects/shop/a.tar", bytes.NewReader(big)); err != nil || n != int64(len(big)) {
		t.Fatalf("put: %d %v", n, err)
	}
	if _, err := b.Put(ctx, "projects/shop/b.tar", strings.NewReader("small")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "projects/other/c.tar", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	list, err := b.List(ctx, "projects/shop")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	if len(list) != 2 || list[0].Key != "projects/shop/a.tar" || list[0].Size != int64(len(big)) || list[1].Key != "projects/shop/b.tar" {
		t.Fatalf("list: %+v", list)
	}
	if empty, err := b.List(ctx, "projects/none"); err != nil || len(empty) != 0 {
		t.Fatalf("missing directory: %v %v", empty, err)
	}
	rc, err := b.Get(ctx, "projects/shop/a.tar")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, big) {
		t.Fatalf("get returned %d bytes, want %d", len(got), len(big))
	}
	if _, err := b.Get(ctx, "projects/shop/nope.tar"); err != ErrObjectNotFound {
		t.Fatalf("missing object: %v", err)
	}
	if err := b.Delete(ctx, "projects/shop/a.tar"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "projects/shop/a.tar"); err != nil {
		t.Fatalf("deleting twice: %v", err)
	}
	if list, _ := b.List(ctx, "projects/shop"); len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// ---- S3 ------------------------------------------------------------------------

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	parts   int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != "backups" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && key == "":
		prefix := q.Get("prefix")
		type content struct {
			Key          string
			Size         int
			LastModified string
		}
		var res struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Contents []content
		}
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Contents = append(res.Contents, content{Key: k, Size: len(f.objects[k]), LastModified: time.Now().UTC().Format(time.RFC3339)})
		}
		_ = xml.NewEncoder(w).Encode(res)
	case r.Method == http.MethodPost && q.Has("uploads"):
		id := strconv.Itoa(len(f.uploads) + 1)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == http.MethodPut && q.Has("partNumber"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		body, _ := io.ReadAll(r.Body)
		f.uploads[q.Get("uploadId")][n] = body
		f.parts++
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
	case r.Method == http.MethodPost && q.Has("uploadId"):
		parts := f.uploads[q.Get("uploadId")]
		var all []byte
		for i := 1; i <= len(parts); i++ {
			all = append(all, parts[i]...)
		}
		f.objects[key] = all
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprint(w, "<CompleteMultipartUploadResult/>")
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
	case r.Method == http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestS3Backend(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b := newS3(Target{Endpoint: srv.URL, Region: "eu-central-003", Bucket: "backups", AccessKey: "AK", SecretKey: "SK", Prefix: "envoryx"})
	// Larger than one part: the multipart path.
	exercise(t, b, randomBytes(s3.PartSize+1234))
	if fake.parts != 2 {
		t.Fatalf("parts uploaded: %d", fake.parts)
	}
	for k := range fake.objects {
		if !strings.HasPrefix(k, "envoryx/") {
			t.Fatalf("object outside the prefix: %s", k)
		}
	}
}

// ---- WebDAV --------------------------------------------------------------------

type fakeDAV struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string]bool
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/dav")
	switch r.Method {
	case "MKCOL":
		d := strings.TrimSuffix(p, "/")
		if f.dirs[d] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.dirs[d] = true
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		dir := p[:strings.LastIndex(p, "/")]
		if !f.dirs[dir] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.files[p], _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		b, ok := f.files[p]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		if _, ok := f.files[p]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.files, p)
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		d := strings.TrimSuffix(p, "/")
		if !f.dirs[d] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav%s/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`, d)
		for name, b := range f.files {
			if strings.HasPrefix(name, d+"/") && !strings.Contains(strings.TrimPrefix(name, d+"/"), "/") {
				fmt.Fprintf(w, `<d:response><d:href>/dav%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:getlastmodified>%s</d:getlastmodified><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, name, len(b), time.Now().UTC().Format(http.TimeFormat))
			}
		}
		for dir := range f.dirs {
			if strings.HasPrefix(dir, d+"/") && !strings.Contains(strings.TrimPrefix(dir, d+"/"), "/") {
				fmt.Fprintf(w, `<d:response><d:href>/dav%s/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`, dir)
			}
		}
		fmt.Fprint(w, `</d:multistatus>`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestWebDAVBackend(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, dirs: map[string]bool{"": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b := newWebDAV(Target{URL: srv.URL + "/dav", User: "me", Password: "secret", Prefix: "envoryx"})
	exercise(t, b, randomBytes(300<<10))

	wrong := newWebDAV(Target{URL: srv.URL + "/dav", User: "me", Password: "nope", Prefix: "envoryx"})
	if _, err := wrong.Put(context.Background(), "x/y.tar", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("wrong password: %v", err)
	}
}

// ---- SFTP ----------------------------------------------------------------------

func TestSFTPBackend(t *testing.T) {
	c1, c2 := net.Pipe()
	srv := sftp.NewRequestServer(c1, sftp.InMemHandler())
	go func() { _ = srv.Serve() }()
	defer srv.Close()
	client, err := sftp.NewClientPipe(c2, c2)
	if err != nil {
		t.Fatal(err)
	}
	b := &sftpBackend{c: client, prefix: "envoryx"}
	defer b.Close()
	exercise(t, b, randomBytes(200<<10))
}
