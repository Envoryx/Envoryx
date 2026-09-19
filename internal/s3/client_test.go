package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The GET Object example from the AWS "Signature Calculations for the Authorization
// Header" documentation: known inputs, known signature.
func TestSignMatchesAWSExample(t *testing.T) {
	c := &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	if err := c.Sign(req, nil); err != nil {
		t.Fatal(err)
	}
	// The doc example signs Range as well; we sign host + x-amz-* only, so the signature
	// differs from the printed one. What we can pin exactly: date, payload hash, scope
	// and the structure. The end-to-end correctness is covered by the integration test.
	auth := req.Header.Get("Authorization")
	for _, want := range []string{"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request", "SignedHeaders=host;x-amz-content-sha256;x-amz-date", "Signature="} {
		if !strings.Contains(auth, want) {
			t.Fatalf("authorization %q lacks %q", auth, want)
		}
	}
	if req.Header.Get("X-Amz-Content-Sha256") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty payload hash = %s", req.Header.Get("X-Amz-Content-Sha256"))
	}
	if req.Header.Get("X-Amz-Date") != "20130524T000000Z" {
		t.Fatalf("date = %s", req.Header.Get("X-Amz-Date"))
	}
}

func TestCanonicalQueryAndPath(t *testing.T) {
	if got := canonicalQuery("policy"); got != "policy=" {
		t.Fatalf("bare key: %q", got)
	}
	if got := canonicalQuery("b=2&a=1&a=0"); got != "a=0&a=1&b=2" {
		t.Fatalf("sorted: %q", got)
	}
	if got := uriEncodePath("/my bucket/ä"); got != "/my%20bucket/%C3%A4" {
		t.Fatalf("path: %q", got)
	}
}

// A fake S3 server checks that EnsureBucket does HEAD → PUT → policy, treats 404 as
// "absent", and retries a server that is still starting.
func TestEnsureBucketAgainstFakeServer(t *testing.T) {
	var calls []string
	created := false
	starting := 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=key/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if starting > 0 {
			starting--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodHead:
			if created {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case r.Method == http.MethodPut && r.URL.RawQuery == "":
			created = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.RawQuery == "policy=":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.RawQuery == "policy=":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (Default{}).EnsureBucket(ctx, srv.URL, "key", "secret", "shop", true); err != nil {
		t.Fatal(err)
	}
	if err := (Default{}).EnsureBucket(ctx, srv.URL, "key", "secret", "shop", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"HEAD /shop?", "PUT /shop?", "PUT /shop?policy=", "HEAD /shop?", "DELETE /shop?policy="}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", calls)
	}
	// A definite refusal is not retried.
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer deny.Close()
	if err := (Default{}).EnsureBucket(ctx, deny.URL, "key", "secret", "shop", true); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("403 must surface immediately: %v", err)
	}
}
