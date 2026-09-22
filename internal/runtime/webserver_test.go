package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The PHP renderings are pinned byte for byte (testdata/webserver/*_php.golden was captured
// before the static branch was added): a change there recreates every PHP project's web
// config, so it has to be deliberate.
func TestWebServerConfigPHPGolden(t *testing.T) {
	for _, variant := range []string{"caddy", "nginx", "apache"} {
		t.Run(variant, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "webserver", variant+"_php.golden"))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := WebServerConfig(variant, "public", WebOptions{PHP: true})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Content != string(want) {
				t.Errorf("%s PHP rendering changed:\n%s", variant, cfg.Content)
			}
			// SPA fallback is meaningless with PHP and must not alter the output.
			spa, _ := WebServerConfig(variant, "public", WebOptions{PHP: true, SPAFallback: true})
			if spa.Content != string(want) {
				t.Errorf("%s: SPAFallback must be ignored with PHP", variant)
			}
			// The exported wrappers stay thin.
			var legacy string
			switch variant {
			case "caddy":
				legacy = Caddyfile("public", true)
			case "nginx":
				legacy = NginxConf("public", true)
			case "apache":
				legacy = HTTPDConf("public", true)
			}
			if legacy != string(want) {
				t.Errorf("%s: exported wrapper diverges", variant)
			}
		})
	}
}

func TestWebServerConfig(t *testing.T) {
	cases := []struct {
		variant  string
		file     string
		target   string
		contains []string
		phpOnly  []string
		static   []string // without PHP, with and without the SPA fallback
		noSPA    []string // without PHP, only when the SPA fallback is off
		spa      []string
	}{
		{
			variant: "caddy", file: "Caddyfile", target: "/etc/caddy/Caddyfile",
			contains: []string{"root * /var/www/html/public", "file_server"},
			phpOnly:  []string{"php_fastcgi php:9000"},
			static:   []string{`@dot path_regexp (^|/)\.`, "respond @dot 404"},
			spa:      []string{"try_files {path} /index.html"},
		},
		{
			variant: "apache", file: "httpd.conf", target: "/usr/local/apache2/conf/httpd.conf",
			contains: []string{`DocumentRoot "/var/www/html/public"`, "AllowOverride All", "mod_rewrite.so", "mod_access_compat.so", "mod_proxy_fcgi.so", `LogFormat "%h %l %u %t \"%r\" %>s %b" common`},
			phpOnly:  []string{`SetHandler "proxy:fcgi://php:9000"`, "index.php", `<Files ".ht*">`},
			static:   []string{`<FilesMatch "^\.">`, `<DirectoryMatch "/\.">`, "DirectoryIndex index.html"},
			spa:      []string{"FallbackResource /index.html"},
		},
		{
			variant: "nginx", file: "default.conf", target: "/etc/nginx/conf.d/default.conf",
			contains: []string{"root /var/www/html/public;", "try_files $uri $uri/"},
			phpOnly:  []string{"fastcgi_pass php:9000;", "/index.php?$query_string", "SCRIPT_FILENAME $document_root$fastcgi_script_name"},
			static:   []string{"index index.html;", `location ~ /\.(?!well-known)`},
			noSPA:    []string{"try_files $uri $uri/ =404;"},
			spa:      []string{"try_files $uri $uri/ /index.html;"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.variant, func(t *testing.T) {
			cfg, err := WebServerConfig(tc.variant, "public", WebOptions{PHP: true})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.FileName != tc.file || cfg.Target != tc.target {
				t.Fatalf("file/target: %+v", cfg)
			}
			for _, s := range append(tc.contains, tc.phpOnly...) {
				if !strings.Contains(cfg.Content, s) {
					t.Errorf("%s config must contain %q:\n%s", tc.variant, s, cfg.Content)
				}
			}
			for _, s := range tc.spa {
				if strings.Contains(cfg.Content, s) {
					t.Errorf("%s PHP config must not contain %q", tc.variant, s)
				}
			}

			static, err := WebServerConfig(tc.variant, "", WebOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.phpOnly {
				if strings.Contains(static.Content, s) {
					t.Errorf("%s config without PHP must not contain %q", tc.variant, s)
				}
			}
			for _, s := range append(tc.static, tc.noSPA...) {
				if !strings.Contains(static.Content, s) {
					t.Errorf("%s static config must contain %q:\n%s", tc.variant, s, static.Content)
				}
			}
			for _, s := range tc.spa {
				if strings.Contains(static.Content, s) {
					t.Errorf("%s static config without SPA fallback must not contain %q", tc.variant, s)
				}
			}
			if strings.Contains(static.Content, "/var/www/html/") {
				t.Errorf("empty docroot must map to /var/www/html:\n%s", static.Content)
			}

			spa, err := WebServerConfig(tc.variant, "dist", WebOptions{SPAFallback: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range append(tc.static, tc.spa...) {
				if !strings.Contains(spa.Content, s) {
					t.Errorf("%s SPA config must contain %q:\n%s", tc.variant, s, spa.Content)
				}
			}
			if !strings.Contains(spa.Content, "/var/www/html/dist") {
				t.Errorf("SPA config must serve the docroot:\n%s", spa.Content)
			}
		})
	}
	if _, err := WebServerConfig("lighttpd", "", WebOptions{PHP: true}); err == nil {
		t.Fatal("unknown variant must fail")
	}
	if !IsWebServer("apache") || IsWebServer("php") {
		t.Fatal("IsWebServer")
	}
}
