package runtime

import (
	"strings"
	"testing"
)

func TestWebServerConfig(t *testing.T) {
	cases := []struct {
		variant  string
		file     string
		target   string
		contains []string
		phpOnly  []string
	}{
		{
			variant: "caddy", file: "Caddyfile", target: "/etc/caddy/Caddyfile",
			contains: []string{"root * /var/www/html/public", "file_server"},
			phpOnly:  []string{"php_fastcgi php:9000"},
		},
		{
			variant: "apache", file: "httpd.conf", target: "/usr/local/apache2/conf/httpd.conf",
			contains: []string{`DocumentRoot "/var/www/html/public"`, "AllowOverride All", "mod_rewrite.so", "mod_access_compat.so", "mod_proxy_fcgi.so", `LogFormat "%h %l %u %t \"%r\" %>s %b" common`},
			phpOnly:  []string{`SetHandler "proxy:fcgi://php:9000"`, "index.php"},
		},
		{
			variant: "nginx", file: "default.conf", target: "/etc/nginx/conf.d/default.conf",
			contains: []string{"root /var/www/html/public;", "try_files $uri $uri/"},
			phpOnly:  []string{"fastcgi_pass php:9000;", "/index.php?$query_string", "SCRIPT_FILENAME $document_root$fastcgi_script_name"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.variant, func(t *testing.T) {
			cfg, err := WebServerConfig(tc.variant, "public", true)
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
			static, err := WebServerConfig(tc.variant, "", false)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.phpOnly {
				if strings.Contains(static.Content, s) {
					t.Errorf("%s config without PHP must not contain %q", tc.variant, s)
				}
			}
			if strings.Contains(static.Content, "/var/www/html/") {
				t.Errorf("empty docroot must map to /var/www/html:\n%s", static.Content)
			}
		})
	}
	if _, err := WebServerConfig("lighttpd", "", true); err == nil {
		t.Fatal("unknown variant must fail")
	}
	if !IsWebServer("apache") || IsWebServer("php") {
		t.Fatal("IsWebServer")
	}
}
