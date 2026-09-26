package runtime

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestExternalDatabaseClientsLogInAsTheProjectUser(t *testing.T) {
	cfg := DatabaseConfig{Host: "db.example.com", Port: 3307, Database: "shop", Username: "shop_app", Password: "p@ss:w/rd"}
	d, _ := DialectFor("mysql")
	argv, env := d.Client(cfg, "SELECT 1")
	if !slices.Equal(argv, []string{"mysql", "-hdb.example.com", "-P3307", "-ushop_app", "-N", "-B", "-e", "SELECT 1"}) || !slices.Equal(env, []string{"MYSQL_PWD=p@ss:w/rd"}) {
		t.Fatalf("mysql client: %v %v", argv, env)
	}
	argv, _ = d.Dump(cfg)
	if !slices.Contains(argv, "--no-tablespaces") || slices.Contains(argv, "--events") || argv[len(argv)-1] != "shop" {
		t.Fatalf("mysqldump against an external server: %v", argv)
	}
	pg, _ := DialectFor("postgresql")
	argv, _ = pg.Client(DatabaseConfig{Host: "10.0.0.5", Port: 5433, Database: "shop", Username: "shop_app"}, "SELECT 1")
	if !slices.Equal(argv[:8], []string{"psql", "-h", "10.0.0.5", "-p", "5433", "-U", "shop_app", "-d"}) || argv[8] != "shop" {
		t.Fatalf("psql against an external server connects to its own database: %v", argv)
	}
	// The project's own containers keep the exact commands they had.
	own := DatabaseConfig{RootPassword: "r00t", Database: "shop", Username: "shop", Password: "pw"}
	argv, env = d.Client(own, "SELECT 1")
	if !slices.Equal(argv, []string{"mysql", "-h127.0.0.1", "-uroot", "-N", "-B", "-e", "SELECT 1"}) || env[0] != "MYSQL_PWD=r00t" {
		t.Fatalf("own container: %v %v", argv, env)
	}
	argv, _ = pg.Client(own, "SELECT 1")
	if !slices.Equal(argv[:6], []string{"psql", "-h", "127.0.0.1", "-U", "shop", "-d"}) || argv[6] != "postgres" {
		t.Fatalf("own postgres: %v", argv)
	}
}

func TestExternalDatabaseEnv(t *testing.T) {
	cfg := DatabaseConfig{Host: "db.example.com", Port: 3307, Database: "shop", Username: "shop_app", Password: "p@ss:w/rd"}
	env := DatabaseEnv(cfg, "mysql")
	if env["DB_HOST"] != "db.example.com" || env["DB_PORT"] != "3307" || env["DB_PASSWORD"] != "p@ss:w/rd" {
		t.Fatalf("env: %v", env)
	}
	if env["DATABASE_URL"] != "mysql://shop_app:p%40ss%3Aw%2Frd@db.example.com:3307/shop" {
		t.Fatalf("DATABASE_URL must escape the password: %s", env["DATABASE_URL"])
	}
	own := DatabaseEnv(DatabaseConfig{Database: "shop", Username: "shop", Password: "aB3dE"}, "postgresql")
	if own["DATABASE_URL"] != "pgsql://shop:aB3dE@database:5432/shop" {
		t.Fatalf("generated credentials must come out unchanged: %s", own["DATABASE_URL"])
	}
}

func TestExternalRedisEnv(t *testing.T) {
	env := RedisEnv(ServiceConfig{Host: "cache.lan", Port: 6380, Password: "s3cr/t"})
	if env["REDIS_HOST"] != "cache.lan" || env["REDIS_PORT"] != "6380" || env["REDIS_PASSWORD"] != "s3cr/t" || env["REDIS_URL"] != "redis://:s3cr%2Ft@cache.lan:6380" {
		t.Fatalf("external redis: %v", env)
	}
	if _, ok := RedisEnv(ServiceConfig{Host: "cache.lan", Port: 6379})["REDIS_PASSWORD"]; ok {
		t.Fatal("no password, no REDIS_PASSWORD")
	}
	if own := RedisEnv(ServiceConfig{}); own["REDIS_URL"] != "redis://redis:6379" || len(own) != 3 {
		t.Fatalf("own redis: %v", own)
	}
}

func TestNormalizeExternalDatabase(t *testing.T) {
	cfg := DatabaseConfig{Host: " DB.Example.com ", Database: "shop-prod", Username: "app@server", Password: "x", RootPassword: "leftover", HostPort: 1234}
	if err := NormalizeExternalDatabase(&cfg, "postgresql"); err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "db.example.com" || cfg.Port != 5432 || cfg.RootPassword != "" || cfg.HostPort != 0 {
		t.Fatalf("normalized: %+v", cfg)
	}
	for name, bad := range map[string]DatabaseConfig{
		"localhost":   {Host: "localhost", Database: "a", Username: "u"},
		"loopback":    {Host: "127.0.0.1", Database: "a", Username: "u"},
		"port":        {Host: "db", Port: 70000, Database: "a", Username: "u"},
		"quote user":  {Host: "db", Database: "a", Username: "u'x"},
		"quote db":    {Host: "db", Database: "a`b", Username: "u"},
		"newline pw":  {Host: "db", Database: "a", Username: "u", Password: "a\nb"},
		"empty host":  {Database: "a", Username: "u"},
		"space in db": {Host: "db", Database: "a b", Username: "u"},
	} {
		if err := NormalizeExternalDatabase(&bad, "mysql"); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s must be refused, got %v", name, err)
		}
	}
	mongo := DatabaseConfig{Host: "db", Database: "a", Username: "u"}
	if err := NormalizeExternalDatabase(&mongo, "mongodb"); err == nil || !strings.Contains(err.Error(), "mariadb, mysql, postgresql") {
		t.Fatalf("mongodb is not offered as external: %v", err)
	}
	r := ServiceConfig{Host: "host.docker.internal"}
	if err := NormalizeExternalRedis(&r); err != nil || r.Port != 6379 {
		t.Fatalf("redis: %+v %v", r, err)
	}
}
