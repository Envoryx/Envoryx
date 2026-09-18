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
