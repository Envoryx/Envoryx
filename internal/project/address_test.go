package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A PRIMARY_SITE_URL holding the old address becomes the reference to ENVORYX_URL; the
// reference itself and an address set on purpose stay as they are.
func TestFollowCraftEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	for _, tc := range []struct{ in, want string }{
		{"A=1\nPRIMARY_SITE_URL=https://blog.test\nB=2\n", "A=1\nPRIMARY_SITE_URL=${ENVORYX_URL}\nB=2\n"},
		{"PRIMARY_SITE_URL=\"https://blog.test/\"\n", "PRIMARY_SITE_URL=${ENVORYX_URL}\n"},
		{"PRIMARY_SITE_URL=${ENVORYX_URL}\n", ""},
		{"PRIMARY_SITE_URL=https://www.example.com\n", ""},
		{"PRIMARY_SITE_URL=https://blog.test.example\n", ""},
	} {
		if err := os.WriteFile(path, []byte(tc.in), 0o640); err != nil {
			t.Fatal(err)
		}
		changed, err := followCraftEnv(dir, "https://blog.test")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if tc.want == "" {
			if changed || string(got) != tc.in {
				t.Fatalf("%q changed to %q", tc.in, got)
			}
			continue
		}
		if !changed || string(got) != tc.want {
			t.Fatalf("%q: got %q (changed %v), want %q", tc.in, got, changed, tc.want)
		}
	}
	if changed, err := followCraftEnv(t.TempDir(), "https://blog.test"); err != nil || changed {
		t.Fatalf("missing .env: %v %v", changed, err)
	}
}

// A .env that is a link out of the project is neither read nor written.
func TestFollowCraftEnvStaysInTheProject(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "secret.env")
	const in = "PRIMARY_SITE_URL=https://blog.test\n"
	if err := os.WriteFile(target, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if changed, err := followCraftEnv(dir, "https://blog.test"); err == nil || changed {
		t.Fatalf("a .env leading out was followed: %v %v", changed, err)
	}
	if got, _ := os.ReadFile(target); string(got) != in {
		t.Fatalf("the file outside changed to %q", got)
	}
}

// Clearing Shopware's cache empties var/cache, but never what a var/cache leading out of
// the project points at.
func TestClearShopwareCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "var", "cache")
	if err := os.MkdirAll(filepath.Join(cache, "prod_abc", "twig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "prod_abc", "twig", "x.php"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := clearShopwareCache(dir); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
		t.Fatalf("var/cache holds %v (%v)", entries, err)
	}

	outside := t.TempDir()
	keep := filepath.Join(outside, "envoryx.db")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cache); err != nil {
		t.Fatal(err)
	}
	if err := clearShopwareCache(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a file outside the project was removed: %v", err)
	}
}

func TestShopwareDomainSQL(t *testing.T) {
	got := shopwareDomainSQL("shop", "https://shop.test/", "https://store.test")
	want := "UPDATE `shop`.sales_channel_domain SET url = CONCAT('https://store.test', SUBSTRING(url, 18)) WHERE url = 'https://shop.test' OR url LIKE 'https://shop.test/%'; SELECT ROW_COUNT();"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// LIKE wildcards and quotes in the address are taken literally.
	if got := shopwareDomainSQL("shop", "http://a_b.test:8080", "http://c.test"); !strings.Contains(got, `LIKE 'http://a\\_b.test:8080/%'`) {
		t.Fatalf("wildcard not escaped: %s", got)
	}
}
