package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	p, err := Parse([]byte(`{"name":"Starter","limits":{"projects":3,"diskGb":1.5},"runtimes":["php","node"],"disabled":["addons"],"settings":{"baseDomain":"c1.example.net"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Starter" || p.Limits.Projects != 3 || p.DiskBytes() != 3<<29 {
		t.Fatalf("plan = %+v", p)
	}
	if p.Allows(FeatureAddons) || !p.Allows(FeatureOffsite) {
		t.Error("addons should be off, offsite on")
	}
	if !p.AllowsRuntime("php") || p.AllowsRuntime("java") {
		t.Error("runtimes: php allowed, java not")
	}
	if err := p.RequireRuntime("java"); !errors.Is(err, ErrQuota) {
		t.Errorf("RequireRuntime(java) = %v", err)
	}
	if v, ok := p.Locked("baseDomain"); !ok || string(v) != `"c1.example.net"` {
		t.Errorf("Locked = %q %v", v, ok)
	}

	for _, bad := range []string{`{"limits":{"projects":-1}}`, `{"disabled":["rockets"]}`, `{"unknown":1}`, `{`, `{"settings":{"sshAuthorizedKeys":"x"}}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s) passed", bad)
		}
	}
}

func TestNilPlanAllowsEverything(t *testing.T) {
	var p *Plan
	if !p.Allows(FeatureAddons) || !p.AllowsRuntime("java") || p.DiskBytes() != 0 {
		t.Error("no plan must limit nothing")
	}
	if _, ok := p.Locked("x"); ok {
		t.Error("no plan locks nothing")
	}
	var h *Holder
	if h.Get() != nil {
		t.Error("nil holder has a plan")
	}
}

func TestHolderReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	h, err := Open(path, nil)
	if err != nil || h.Get() != nil {
		t.Fatalf("missing file: %v %v", h.Get(), err)
	}
	if err := os.WriteFile(path, []byte(`{"name":"A","limits":{"projects":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.Reload()
	if p := h.Get(); p == nil || p.Name != "A" {
		t.Fatalf("after write: %+v", p)
	}
	// A broken file keeps the previous plan.
	if err := os.WriteFile(path, []byte(`{"limits":`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.Reload()
	if p := h.Get(); p == nil || p.Name != "A" || h.Err() == nil {
		t.Fatalf("broken file: %+v %v", p, h.Err())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	h.Reload()
	if h.Get() != nil || h.Err() != nil {
		t.Fatalf("removed file: %+v %v", h.Get(), h.Err())
	}
	// A broken file at start refuses to start.
	if err := os.WriteFile(path, []byte(`nope`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, nil); err == nil {
		t.Error("Open of a broken file passed")
	}
}
