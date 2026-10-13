package provenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang/snappy"
	"github.com/sigstore/sigstore-go/pkg/root"
)

// testdata/actions-runner.json is what the GitHub API answered (without the bundle_url,
// a signed download link) for the attestations of
// ghcr.io/actions/actions-runner@sha256:4ffadc00... (built by actions/runner's
// release.yml on refs/heads/releases/m338); trusted-root.json is the public-good root
// from sigstore-go's examples.
const (
	runnerDigest = "sha256:4ffadc0002b2581327e06101fc8c06cd189232baf79fe561fac9caeb76f5e807"
	runnerRef    = "refs/heads/releases/m338"
)

func testVerifier(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *Verifier {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(answer))
	t.Cleanup(srv.Close)
	return &Verifier{
		Repository: "actions/runner",
		Workflow:   ".github/workflows/release.yml",
		API:        srv.URL,
		HTTP:       srv.Client(),
		TrustedRoot: func() (root.TrustedMaterial, error) {
			return root.NewTrustedRootFromPath("testdata/trusted-root.json")
		},
		ref: func(string) string { return runnerRef },
	}
}

func serveFile(t *testing.T, name string) func(http.ResponseWriter, *http.Request) {
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/actions/runner/attestations/"+runnerDigest {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("predicate_type") != predicateType {
			t.Errorf("predicate_type = %q", r.URL.Query().Get("predicate_type"))
		}
		_, _ = w.Write(b)
	}
}

func TestVerifyAcceptsTheWorkflowsAttestation(t *testing.T) {
	v := testVerifier(t, serveFile(t, "testdata/actions-runner.json"))
	if err := v.Verify(context.Background(), runnerDigest, "2.330.0"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRefusesOtherBuilds(t *testing.T) {
	ctx := context.Background()
	for name, mod := range map[string]func(v *Verifier){
		"another workflow":   func(v *Verifier) { v.Workflow = ".github/workflows/docker.yml" },
		"another ref":        func(v *Verifier) { v.ref = func(version string) string { return "refs/tags/v" + version } },
		"another repository": func(v *Verifier) { v.Repository = "actions/runner-fork" },
	} {
		t.Run(name, func(t *testing.T) {
			v := testVerifier(t, func(w http.ResponseWriter, r *http.Request) {
				// The answer for the real repository, whatever is asked.
				b, _ := os.ReadFile("testdata/actions-runner.json")
				_, _ = w.Write(b)
			})
			mod(v)
			if err := v.Verify(ctx, runnerDigest, "2.330.0"); err == nil {
				t.Fatal("Verify accepted the attestation")
			}
		})
	}
	t.Run("another digest", func(t *testing.T) {
		other := "sha256:" + strings.Repeat("ab", 32)
		v := testVerifier(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := os.ReadFile("testdata/actions-runner.json")
			_, _ = w.Write(b)
		})
		if err := v.Verify(ctx, other, "2.330.0"); err == nil {
			t.Fatal("Verify accepted an attestation of another digest")
		}
	})
	t.Run("no attestation", func(t *testing.T) {
		v := testVerifier(t, http.NotFound)
		err := v.Verify(ctx, runnerDigest, "2.330.0")
		if err == nil || !strings.Contains(err.Error(), "no build attestation") {
			t.Fatalf("Verify = %v", err)
		}
	})
	t.Run("API error", func(t *testing.T) {
		v := testVerifier(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		})
		err := v.Verify(ctx, runnerDigest, "2.330.0")
		if err == nil || !strings.Contains(err.Error(), "rate limit") {
			t.Fatalf("Verify = %v", err)
		}
	})
	t.Run("bad digest", func(t *testing.T) {
		v := testVerifier(t, http.NotFound)
		if err := v.Verify(ctx, "sha256:abc", "2.330.0"); err == nil {
			t.Fatal("Verify accepted a short digest")
		}
	})
}

// GitHub may hand out a bundle as a snappy-compressed download instead of inline.
func TestVerifyFetchesBundleURL(t *testing.T) {
	att, err := os.ReadFile("testdata/actions-runner.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Attestations []struct {
			Bundle json.RawMessage `json:"bundle"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(att, &resp); err != nil || len(resp.Attestations) == 0 {
		t.Fatalf("testdata: %v", err)
	}
	var v *Verifier
	v = testVerifier(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundle" {
			_, _ = w.Write(snappy.Encode(nil, resp.Attestations[0].Bundle))
			return
		}
		_, _ = w.Write([]byte(`{"attestations":[{"bundle":null,"bundle_url":"` + v.API + `/bundle"}]}`))
	})
	if err := v.Verify(context.Background(), runnerDigest, "2.330.0"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}
