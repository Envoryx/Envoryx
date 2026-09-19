//go:build integration

package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Against a real RustFS container: the signature is accepted, the bucket appears, the
// public-read policy makes anonymous reads work and its removal closes them again.
func TestProvisionAgainstRustFS(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("envoryx-s3-test-%d", time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:0:9000", "-e", "RUSTFS_ACCESS_KEY=testkey", "-e", "RUSTFS_SECRET_KEY=testsecret123456", "rustfs/rustfs:1.0.0")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	port := run("port", name, "9000/tcp")
	endpoint := "http://" + strings.Split(port, "\n")[0]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := (Default{}).EnsureBucket(ctx, endpoint, "testkey", "testsecret123456", "shop", true); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	c := &Client{Endpoint: endpoint, Region: "us-east-1", AccessKey: "testkey", SecretKey: "testsecret123456"}
	res, err := c.do(ctx, http.MethodPut, "/shop/hello.txt", "", []byte("hi"))
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	res.Body.Close()
	anon := func() int {
		r, err := http.Get(endpoint + "/shop/hello.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		io.Copy(io.Discard, r.Body)
		return r.StatusCode
	}
	if code := anon(); code != http.StatusOK {
		t.Fatalf("anonymous read with public policy = %d", code)
	}
	// Idempotent, and flipping public read off closes the bucket again.
	if err := (Default{}).EnsureBucket(ctx, endpoint, "testkey", "testsecret123456", "shop", false); err != nil {
		t.Fatalf("ensure bucket (private): %v", err)
	}
	if code := anon(); code != http.StatusForbidden {
		t.Fatalf("anonymous read after removing the policy = %d", code)
	}
	// Objects round-trip: streamed put (unsigned payload), listing across pages, get with
	// content type, batch delete.
	var keys []string
	for i := 0; i < 1203; i++ {
		k := fmt.Sprintf("many/obj-%04d.txt", i)
		keys = append(keys, k)
		if err := c.PutObject(ctx, "shop", k, strings.NewReader("x"), 1, "text/plain"); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	big := strings.Repeat("0123456789", 700000) // 7 MB
	if err := c.PutObject(ctx, "shop", "img/big.bin", strings.NewReader(big), int64(len(big)), "application/octet-stream"); err != nil {
		t.Fatalf("put big: %v", err)
	}
	list, err := c.ListObjects(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1203+2 { // + hello.txt + big.bin
		t.Fatalf("listed %d objects", len(list))
	}
	body, ctype, err := c.GetObject(ctx, "shop", "img/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != big || ctype != "application/octet-stream" {
		t.Fatalf("get big: %d bytes, type %q", len(got), ctype)
	}
	if err := c.DeleteObjects(ctx, "shop", append(keys, "img/big.bin")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := c.ListObjects(ctx, "shop"); len(list) != 1 {
		t.Fatalf("after delete: %d objects left", len(list))
	}

	// Wrong credentials are refused, not retried forever.
	short, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	if err := (Default{}).EnsureBucket(short, endpoint, "testkey", "wrong", "shop", false); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("bad secret: %v", err)
	}
}
