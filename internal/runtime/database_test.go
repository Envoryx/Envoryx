package runtime

import (
	"strings"
	"testing"
)

func TestMongoDialectKeepsPasswordOutOfClientArgv(t *testing.T) {
	d, ok := DialectFor("mongodb")
	if !ok {
		t.Fatal("mongodb dialect missing")
	}
	cfg := DatabaseConfig{Database: "shop", Username: "shop", Password: "s3cretPW"}
	argv, env := d.Client(cfg, d.ListDatabases)
	for _, a := range argv {
		if strings.Contains(a, "s3cretPW") {
			t.Fatalf("password must not appear in argv: %v", argv)
		}
	}
	if len(env) != 1 || !strings.HasPrefix(env[0], "STAQIO_MONGO_URI=mongodb://shop:s3cretPW@127.0.0.1:27017/admin") {
		t.Fatalf("env: %v", env)
	}
	got := DatabaseEnv(cfg, "mongodb")
	if got["DATABASE_URL"] != "mongodb://shop:s3cretPW@database:27017/shop?authSource=admin" || got["MONGODB_URI"] == "" || got["DB_CONNECTION"] != "mongodb" || got["DB_PORT"] != "27017" {
		t.Fatalf("injected env: %v", got)
	}
	if !IsSystemDatabase("admin") || !IsSystemDatabase("local") || IsSystemDatabase("shop") {
		t.Fatal("mongo system databases must be hidden")
	}
	if strings.Contains(strings.Join(d.Health, " "), "s3cret") {
		t.Fatal("healthcheck must not need credentials")
	}
}

func TestXdebugINI(t *testing.T) {
	cfg := DefaultPHPConfig()
	cfg.Xdebug = true
	cfg.XdebugIDEKey = "vscode"
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	ini := cfg.INIWith("8.4", INIOptions{XdebugClientHost: "192.168.1.20"})
	for _, want := range []string{"zend_extension=xdebug", "xdebug.mode=debug,develop", "xdebug.client_host=192.168.1.20", "xdebug.idekey=vscode", "xdebug.client_discovery_header=HTTP_X_FORWARDED_FOR"} {
		if !strings.Contains(ini, want) {
			t.Fatalf("ini missing %q:\n%s", want, ini)
		}
	}
	cfg.XdebugClientHost = "dev.lan"
	if !strings.Contains(cfg.INIWith("8.4", INIOptions{XdebugClientHost: "192.168.1.20"}), "xdebug.client_host=dev.lan") {
		t.Fatal("project override must win")
	}
	cfg.Xdebug = false
	if strings.Contains(cfg.INI("8.4"), "xdebug") {
		t.Fatal("disabled xdebug must not appear")
	}
	bad := DefaultPHPConfig()
	bad.XdebugIDEKey = "bad key;"
	if err := bad.Normalize(); err == nil {
		t.Fatal("invalid ide key must be rejected")
	}
	bad = DefaultPHPConfig()
	bad.XdebugClientHost = "not a host"
	if err := bad.Normalize(); err == nil {
		t.Fatal("invalid client host must be rejected")
	}
}
