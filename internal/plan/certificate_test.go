package plan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/plantest"
)

func TestParseCertificate(t *testing.T) {
	c := plantest.Certificate(t, "kunde.envbay.dev")
	got, err := plan.ParseCertificate([]byte(plantest.JSON(t, c)))
	if err != nil || got.Domain != "kunde.envbay.dev" || got.NotAfter().IsZero() {
		t.Fatalf("a fitting certificate: %+v %v", got, err)
	}
	other := plantest.Certificate(t, "other.envbay.dev")
	for name, bad := range map[string]plan.Certificate{
		"another domain": {Domain: "kunde.envbay.dev", Certificate: other.Certificate, Key: other.Key},
		"another key":    {Domain: "kunde.envbay.dev", Certificate: c.Certificate, Key: other.Key},
		"no domain":      {Certificate: c.Certificate, Key: c.Key},
	} {
		if _, err := plan.ParseCertificate([]byte(plantest.JSON(t, bad))); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	if _, err := plan.ParseCertificate([]byte(`{"domain":"kunde.envbay.dev","extra":1}`)); err == nil {
		t.Error("an unknown field was taken")
	}
}

// The holder picks the certificate up, keeps it when a broken one comes and lets it go
// with the file; a broken file at start doesn't keep the instance down.
func TestHolderCertificate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, plan.CertificateFile)
	h, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil || h.Certificate() != nil {
		t.Fatalf("missing file: %v %v", h.Certificate(), err)
	}
	var seen []*plan.Certificate
	h.OnCertificate(func(c *plan.Certificate) { seen = append(seen, c) })
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(plantest.JSON(t, plantest.Certificate(t, "kunde.envbay.dev")))
	h.Reload()
	h.Reload()
	if c := h.Certificate(); c == nil || c.Domain != "kunde.envbay.dev" || len(seen) != 1 {
		t.Fatalf("after write: %+v, %d callbacks", c, len(seen))
	}
	write(`{"domain":"kunde.envbay.dev"}`)
	h.Reload()
	if h.Certificate() == nil || len(seen) != 1 {
		t.Fatal("a broken file took the certificate away")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	h.Reload()
	if h.Certificate() != nil || len(seen) != 2 || seen[1] != nil {
		t.Fatalf("removed file: %+v, %d callbacks", h.Certificate(), len(seen))
	}
	write(`{`)
	if h, err := plan.Open(filepath.Join(dir, plan.File), nil); err != nil || h.Certificate() != nil {
		t.Fatalf("Open with a broken certificate: %v", err)
	}
}
