// Package provenance checks that an image was built by the Envoryx release workflow before
// an Envoryx updates itself to it. The workflow attests every image it pushes: GitHub signs
// a SLSA provenance statement for the image digest with a short-lived Sigstore certificate
// that names the workflow and the tag it ran for, and keeps the attestation behind its API.
// Verify fetches the attestations of a digest and accepts the image only when one of them
// carries a valid signature from that workflow on that release's tag.
package provenance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang/snappy"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	// Repository is the GitHub repository whose workflow builds the Envoryx images.
	Repository = "Envoryx/Envoryx"
	// Workflow builds and attests the images.
	Workflow = ".github/workflows/docker.yml"

	issuer        = "https://token.actions.githubusercontent.com"
	predicateType = "https://slsa.dev/provenance/v1"
	// maxResponse caps an answer of the attestation API or a bundle download.
	maxResponse = 8 << 20
)

// Verifier checks image digests against the attestations of a repository's workflow.
type Verifier struct {
	// Repository and Workflow name the build that must have signed (Repository and
	// Workflow when empty).
	Repository string
	Workflow   string
	// API is the GitHub API (https://api.github.com when empty).
	API  string
	HTTP *http.Client
	// TrustedRoot returns Sigstore's keys and certificates; nil loads the public-good
	// instance's through TUF, cached in CacheDir.
	TrustedRoot func() (root.TrustedMaterial, error)
	CacheDir    string

	// ref is the git ref the workflow must have run for (refs/tags/v<version> when nil).
	ref func(version string) string

	mu      sync.Mutex
	trusted root.TrustedMaterial
}

// Verify returns nil when digest (sha256:<hex>) has an attestation signed by the workflow
// for the tag v<version>.
func (v *Verifier) Verify(ctx context.Context, digest, version string) error {
	sum, ok := strings.CutPrefix(digest, "sha256:")
	raw, err := hex.DecodeString(sum)
	if !ok || err != nil || len(raw) != 32 {
		return fmt.Errorf("%q is no sha256 image digest", digest)
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || strings.ContainsAny(version, "/@ ") {
		return fmt.Errorf("%q is no version", version)
	}
	bundles, err := v.fetch(ctx, digest)
	if err != nil {
		return err
	}
	if len(bundles) == 0 {
		return fmt.Errorf("%s has no build attestation from %s: it wasn't built by its release workflow", digest, v.repository())
	}
	trusted, err := v.trustedRoot()
	if err != nil {
		return fmt.Errorf("loading Sigstore's trusted root: %w", err)
	}
	verifier, err := verify.NewVerifier(trusted, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	// The certificate must name the workflow on this release's tag, run on GitHub's own
	// runners: a build of a branch, another tag or another workflow doesn't count.
	ref := "refs/tags/v" + version
	if v.ref != nil {
		ref = v.ref(version)
	}
	san := "https://github.com/" + v.repository() + "/" + v.workflow() + "@" + ref
	id, err := verify.NewCertificateIdentity(
		verify.SubjectAlternativeNameMatcher{Regexp: *regexp.MustCompile("^(?i:" + regexp.QuoteMeta(san) + ")$")},
		verify.IssuerMatcher{Issuer: issuer},
		certificate.Extensions{RunnerEnvironment: "github-hosted"})
	if err != nil {
		return err
	}
	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", raw), verify.WithCertificateIdentity(id))
	var errs []error
	for _, b := range bundles {
		res, err := verifier.Verify(b, policy)
		if err == nil && (res.Statement == nil || res.Statement.GetPredicateType() != predicateType) {
			err = errors.New("the attestation is no SLSA build provenance")
		}
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return fmt.Errorf("no attestation of %s proves a build by %s for v%s: %w", digest, san, version, errors.Join(errs...))
}

func (v *Verifier) repository() string {
	if v.Repository != "" {
		return v.Repository
	}
	return Repository
}

func (v *Verifier) workflow() string {
	if v.Workflow != "" {
		return v.Workflow
	}
	return Workflow
}

func (v *Verifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

func (v *Verifier) trustedRoot() (root.TrustedMaterial, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.trusted != nil {
		return v.trusted, nil
	}
	load := v.TrustedRoot
	if load == nil {
		load = v.publicGood
	}
	tm, err := load()
	if err != nil {
		return nil, err
	}
	v.trusted = tm
	return tm, nil
}

// publicGood loads the public-good Sigstore's trusted root through TUF, starting from
// the root sigstore-go embeds; GitHub signs attestations of public repositories there.
func (v *Verifier) publicGood() (root.TrustedMaterial, error) {
	opts := tuf.DefaultOptions()
	if v.CacheDir != "" {
		if err := os.MkdirAll(v.CacheDir, 0o700); err != nil {
			return nil, err
		}
		opts.CachePath = v.CacheDir
	} else {
		opts.DisableLocalCache = true
	}
	c, err := tuf.New(opts)
	if err != nil {
		return nil, err
	}
	return root.GetTrustedRoot(c)
}

// fetch returns the provenance bundles GitHub keeps for digest.
func (v *Verifier) fetch(ctx context.Context, digest string) ([]*bundle.Bundle, error) {
	api := strings.TrimRight(v.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	u := api + "/repos/" + v.repository() + "/attestations/" + url.PathEscape(digest) +
		"?per_page=30&predicate_type=" + url.QueryEscape(predicateType)
	body, status, err := v.get(ctx, u, true)
	if err != nil {
		return nil, fmt.Errorf("asking GitHub for the attestations of %s: %w", digest, err)
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("asking GitHub for the attestations of %s: %s", digest, statusText(status, body))
	}
	var resp struct {
		Attestations []struct {
			Bundle    json.RawMessage `json:"bundle"`
			BundleURL string          `json:"bundle_url"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("the attestations of %s: %w", digest, err)
	}
	var out []*bundle.Bundle
	for _, a := range resp.Attestations {
		raw := []byte(a.Bundle)
		if len(raw) == 0 || string(raw) == "null" {
			// Large bundles come as a snappy-compressed download instead.
			if !strings.HasPrefix(a.BundleURL, "https://") {
				continue
			}
			b, status, err := v.get(ctx, a.BundleURL, false)
			if err != nil || status != http.StatusOK {
				continue
			}
			if raw, err = snappy.Decode(nil, b); err != nil {
				continue
			}
		}
		var b bundle.Bundle
		if err := b.UnmarshalJSON(raw); err != nil {
			continue
		}
		out = append(out, &b)
	}
	return out, nil
}

func (v *Verifier) get(ctx context.Context, u string, api bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, 0, err
	}
	if len(b) > maxResponse {
		return nil, 0, errors.New("the answer is too large")
	}
	return b, resp.StatusCode, nil
}

func statusText(status int, body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Message != "" {
		return fmt.Sprintf("%d %s", status, e.Message)
	}
	return fmt.Sprintf("status %d", status)
}
