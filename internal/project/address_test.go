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
		changed, err := followCraftEnv(path, "https://blog.test")
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
	if changed, err := followCraftEnv(filepath.Join(dir, "missing"), "https://blog.test"); err != nil || changed {
		t.Fatalf("missing .env: %v %v", changed, err)
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
