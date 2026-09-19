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
	// Wrong credentials are refused, not retried forever.
	short, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	if err := (Default{}).EnsureBucket(short, endpoint, "testkey", "wrong", "shop", false); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("bad secret: %v", err)
	}
}
