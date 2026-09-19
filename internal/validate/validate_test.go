package validate

import (
	"errors"
	"testing"
)

func TestSlug(t *testing.T) {
	ok := []string{"a", "acme-shop", "abc123", "a-b-c", "x1234567890123456789012345678901234567890"[:40]}
	for _, s := range ok {
		if err := Slug(s); err != nil {
			t.Errorf("Slug(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{"", "-abc", "abc-", "Abc", "a b", "a_b", "a.b", "../x", "x/y", "a" + string(make([]byte, 41))}
	for _, s := range bad {
		if err := Slug(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Slug(%q) expected ErrInvalid, got %v", s, err)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Shop":       "acme-shop",
		"  My_Project.v2": "my-project-v2",
		"Ünïcode Name":    "ncode-name",
		"---":             "",
		"legacy/shop":     "legacy-shop",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelativePath(t *testing.T) {
	good := map[string]string{
		"acme-shop":        "acme-shop",
		"clients/acme/web": "clients/acme/web",
		"./shop":           "shop",
		"shop/":            "shop",
		"a\\b":             "a/b",
	}
	for in, want := range good {
		got, err := RelativePath(in, 3)
		if err != nil || got != want {
			t.Errorf("RelativePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "/abs", "../etc", "a/../../b", "..", ".", "a/./../..", ".hidden", "a/b/c/d", "with space", "nul\x00byte", "a//b/../../../etc"}
	for _, in := range bad {
		if _, err := RelativePath(in, 3); !errors.Is(err, ErrInvalid) {
			t.Errorf("RelativePath(%q) expected ErrInvalid, got %v", in, err)
		}
	}
}

func TestResolveUnder(t *testing.T) {
	if p, err := ResolveUnder("/projects", "shop"); err != nil || p != "/projects/shop" {
		t.Fatalf("unexpected %q %v", p, err)
	}
	if _, err := ResolveUnder("/projects", "../etc"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected escape to be rejected, got %v", err)
	}
}

func TestEnv(t *testing.T) {
	if err := EnvKey("APP_ENV"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"", "1ABC", "app_env", "A-B", "A B", "A=B"} {
		if err := EnvKey(k); !errors.Is(err, ErrInvalid) {
			t.Errorf("EnvKey(%q) expected error", k)
		}
	}
	if err := EnvValue("line\nbreak"); !errors.Is(err, ErrInvalid) {
		t.Error("expected line break rejection")
	}
	if err := EnvValue("ok value with spaces=and symbols"); err != nil {
		t.Error(err)
	}
}

func TestUUIDAndVersion(t *testing.T) {
	if err := UUID("3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f"); err != nil {
		t.Fatal(err)
	}
	if err := UUID("not-a-uuid"); !errors.Is(err, ErrInvalid) {
		t.Fatal("expected invalid uuid")
	}
	if err := Version("8.4"); err != nil {
		t.Fatal(err)
	}
	if err := Version("8.4; rm -rf /"); !errors.Is(err, ErrInvalid) {
		t.Fatal("expected invalid version")
	}
}

func TestProjectNameAndUsername(t *testing.T) {
	if err := ProjectName("Acme Shop"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", " lead", "trail ", "ctrl\x01char", string(make([]byte, 65))} {
		if err := ProjectName(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("ProjectName(%q) expected error", n)
		}
	}
	if err := Username("admin"); err != nil {
		t.Fatal(err)
	}
	if err := Username("a"); !errors.Is(err, ErrInvalid) {
		t.Fatal("expected short username rejection")
	}
}
