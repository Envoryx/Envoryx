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
	if len(env) != 1 || !strings.HasPrefix(env[0], "ENVORYX_MONGO_URI=mongodb://shop:s3cretPW@127.0.0.1:27017/admin") {
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

// Doctrine DBAL reads the version as serverVersion: MariaDB needs its prefix and three
// numbers, a rolling tag counts as its first release.
func TestDatabaseServerVersion(t *testing.T) {
	cases := []struct{ variant, version, want string }{
		{"postgresql", "18", "18"},
		{"postgresql", "16.4", "16.4"},
		{"mysql", "8.4", "8.4.0"},
		{"mysql", "9", "9.0.0"},
		{"mysql", "8.0.39", "8.0.39"},
		{"mariadb", "11", "mariadb-11.0.0"},
		{"mariadb", "10.11", "mariadb-10.11.0"},
		{"mariadb", "11.4.2-noble", "mariadb-11.4.2"},
		{"mariadb", "latest", ""},
		{"postgresql", "", ""},
		{"mongodb", "8", ""},
	}
	for _, c := range cases {
		if got := DatabaseServerVersion(c.variant, c.version); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.variant, c.version, got, c.want)
		}
	}
	cfg := DatabaseConfig{Database: "shop", Username: "shop", Password: "pw"}
	if env := DatabaseEnvFor(cfg, "mariadb", "11.4", PrimaryDatabaseHost, ""); env["DB_SERVER_VERSION"] != "mariadb-11.4.0" {
		t.Fatalf("primary: %v", env)
	}
	if env := DatabaseEnvFor(cfg, "postgresql", "17", "reports", "REPORTS"); env["REPORTS_DB_SERVER_VERSION"] != "17" {
		t.Fatalf("additional: %v", env)
	}
	if _, ok := DatabaseEnvFor(cfg, "mongodb", "8", PrimaryDatabaseHost, "")["DB_SERVER_VERSION"]; ok {
		t.Fatal("MongoDB has no Doctrine server version")
	}
	// An external server's version is unknown: the catalogue version was never chosen.
	cfg.Host, cfg.Port = "db.example.com", 3306
	if _, ok := DatabaseEnvFor(cfg, "mysql", "8.4", PrimaryDatabaseHost, "")["DB_SERVER_VERSION"]; ok {
		t.Fatal("external database must not get DB_SERVER_VERSION")
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
	cfg.XdebugMode = "trigger"
	if ini := cfg.INI("8.4"); !strings.Contains(ini, "xdebug.start_with_request=trigger") {
		t.Fatalf("trigger mode: %s", ini)
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

func TestPostgresDataDirFollowsTheImageLayout(t *testing.T) {
	pg, ok := DialectFor("postgresql")
	if !ok {
		t.Fatal("no postgresql dialect")
	}
	// 18 moved the cluster into /var/lib/postgresql/<major>/docker and refuses to start
	// with a volume on the old path; 16 and 17 keep the data directory itself.
	for version, want := range map[string]string{
		"16": "/var/lib/postgresql/data",
		"17": "/var/lib/postgresql/data",
		"18": "/var/lib/postgresql",
		"19": "/var/lib/postgresql",
		"":   "/var/lib/postgresql/data",
	} {
		if got := pg.DataDirTarget(version); got != want {
			t.Errorf("postgres %q data dir = %q, want %q", version, got, want)
		}
	}
	// A dialect without an override ignores the version.
	my, _ := DialectFor("mysql")
	if got := my.DataDirTarget("9"); got != "/var/lib/mysql" {
		t.Errorf("mysql data dir = %q", got)
	}
}

func TestDBIdentifierAvoidsNamesTheServersRefuse(t *testing.T) {
	for slug, want := range map[string]string{
		"acme-shop":                             "acme_shop",
		"pg-probe":                              "app_pg_probe", // PostgreSQL refuses roles starting with pg_
		"pgadmin":                               "pgadmin",
		"postgres":                              "app_postgres",
		"mysql":                                 "app_mysql",
		"admin":                                 "app_admin", // MongoDB's system database
		"root":                                  "app_root",  // MySQL's image refuses root as MYSQL_USER
		"2024-promo":                            "app_2024_promo",
		"a-very-long-project-name-that-goes-on": "a_very_long_project_name_that_go",
		"9-a-very-long-project-name-that-goes":  "app_9_a_very_long_project_name_t",
	} {
		got := DBIdentifier(slug)
		if got != want {
			t.Errorf("DBIdentifier(%q) = %q, want %q", slug, got, want)
		}
		if len(got) > 32 {
			t.Errorf("DBIdentifier(%q) = %q is longer than 32", slug, got)
		}
	}
}

func TestMongoToolsNameNoDatabaseInTheURI(t *testing.T) {
	d, _ := DialectFor("mongodb")
	c := DatabaseConfig{Database: "shop", Username: "shop", Password: "pw"}
	// mongodump 100.17 and later refuse a database in the URI that differs from --db.
	for name, argv := range map[string][]string{"dump": first(d.Dump(c)), "restore": first(d.Restore(c)), "restore into": first(d.RestoreInto(c, "staging"))} {
		uri := argv[len(argv)-1]
		if !strings.Contains(uri, "@127.0.0.1:27017/?authSource=admin") {
			t.Errorf("%s: uri %q must name no database", name, uri)
		}
	}
}

func first(argv, _ []string) []string { return argv }
