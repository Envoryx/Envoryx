package siteimport

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/validate"
)

var testOptions = Options{PHPVersions: []string{"8.5", "8.4", "8.3", "8.2", "8.1", "8.0", "7.4"}, DefaultPHP: "8.5"}

type member struct {
	name, body, link string
}

func writeZip(t *testing.T, members ...member) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		if m.link != "" {
			h := &zip.FileHeader{Name: m.name}
			h.SetMode(os.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(m.link))
			continue
		}
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(m.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "site.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeTarGz(t *testing.T, members ...member) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		hdr := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}
		switch {
		case m.link != "":
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, m.link, 0
		case strings.HasSuffix(m.name, "/"):
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(m.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(t.TempDir(), "site.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatches(t *testing.T) {
	cases := []struct {
		constraint string
		major      int
		minor      int
		want       bool
	}{
		{"^7.4 || ^8.0", 8, 3, true},
		{"^7.4 || ^8.0", 7, 3, false},
		{"^8.1", 8, 0, false},
		{"^8.1", 8, 5, true},
		{"^8.1", 9, 0, false},
		{">=8.1 <8.4", 8, 4, false},
		{">=8.1 <8.4", 8, 3, true},
		{">= 8.1, < 8.3", 8, 2, true},
		{">= 8.1, < 8.3", 8, 3, false},
		{"~8.2.0", 8, 2, true},
		{"~8.2.0", 8, 3, false},
		{"~8.2", 8, 5, true},
		{"7.4 - 8.1", 8, 1, true},
		{"7.4 - 8.1", 8, 2, false},
		{"8.*", 8, 4, true},
		{"7.*", 8, 0, false},
		{"^7.2|^8.0", 7, 4, true},
		{">=5.6", 8, 5, true},
		{"", 8, 5, true},
		{"^8.2.1@dev", 8, 2, true},
		{"8.2.*", 8, 2, true},
		{"8.2.*", 8, 3, false},
	}
	for _, c := range cases {
		if got := matches(c.constraint, c.major, c.minor); got != c.want {
			t.Errorf("matches(%q, %d.%d) = %v, want %v", c.constraint, c.major, c.minor, got, c.want)
		}
	}
}

func TestCommonRoot(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"public_html", "public_html/index.php", "public_html/css/a.css"}, "public_html/"},
		{[]string{"backup/httpdocs/index.php", "backup/httpdocs/x/y.php"}, "backup/httpdocs/"},
		{[]string{"index.php", "css/a.css"}, ""},
		{[]string{"site/index.php", "other/x"}, ""},
		// One file at the top is the site itself.
		{[]string{"index.html"}, ""},
		{[]string{"site/index.html"}, "site/"},
	}
	for _, c := range cases {
		if got := commonRoot(c.names); got != c.want {
			t.Errorf("commonRoot(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}

func TestExtractStripsRootAndStaysInside(t *testing.T) {
	archive := writeZip(t,
		member{name: "public_html/index.php", body: "<?php echo 1;"},
		member{name: "public_html/inc/db.php", body: "<?php"},
		member{name: "public_html/inside", link: "inc/db.php"},
		member{name: "public_html/outside", link: "../../etc/passwd"},
		member{name: "__MACOSX/public_html/._index.php", body: "junk"},
	)
	target := filepath.Join(t.TempDir(), "proj")
	if err := Extract(archive, target, "public_html/", -1, -1); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(target, "index.php")); err != nil || string(b) != "<?php echo 1;" {
		t.Fatalf("index.php = %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "inside")); err != nil {
		t.Errorf("a link inside the project must be kept: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "outside")); !os.IsNotExist(err) {
		t.Errorf("a link out of the project must be left out, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "__MACOSX")); !os.IsNotExist(err) {
		t.Error("__MACOSX must not be unpacked")
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	for name, archive := range map[string]string{
		"zip dotdot":  writeZip(t, member{name: "../evil.php", body: "x"}),
		"tar dotdot":  writeTarGz(t, member{name: "a/../../evil.php", body: "x"}),
		"tar through": writeTarGz(t, member{name: "link", link: "."}, member{name: "up", link: ".."}, member{name: "up/evil.php", body: "x"}),
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "proj")
			err := Extract(archive, target, "", -1, -1)
			if _, serr := os.Stat(filepath.Join(parent, "evil.php")); serr == nil {
				t.Fatal("a file was written outside the project")
			}
			if name != "tar through" && !errors.Is(err, validate.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestExtractBudget(t *testing.T) {
	old := MaxExtractBytes
	MaxExtractBytes = 10
	defer func() { MaxExtractBytes = old }()
	archive := writeTarGz(t, member{name: "a.txt", body: "0123456789abcdef"})
	if err := Extract(archive, t.TempDir(), "", -1, -1); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("want ErrInvalid for an archive over the budget, got %v", err)
	}
}

func TestSniffRefusesOtherFiles(t *testing.T) {
	if _, err := Analyze(writeFile(t, "x.rar", "Rar!\x1a\x07"), "", testOptions); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestAnalyzeWordPress(t *testing.T) {
	site := writeZip(t,
		member{name: "httpdocs/wp-config.php", body: "<?php define('DB_NAME', 'old');"},
		member{name: "httpdocs/wp-includes/version.php", body: "<?php\n$wp_version = '6.2.2';\n"},
		member{name: "httpdocs/index.php", body: "<?php"},
		member{name: "httpdocs/.htaccess", body: "RewriteEngine On"},
	)
	dump := writeFile(t, "dump.sql", "-- MySQL dump 10.13  Distrib 8.0.35, for Linux (x86_64)\n--\n-- Server version\t8.0.35\n\nCREATE TABLE `wp_options` (id int) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;\n")
	a, err := Analyze(site, dump, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Framework.ID != "wordpress" || a.Framework.Version != "6.2.2" {
		t.Errorf("framework = %+v", a.Framework)
	}
	if a.Root != "httpdocs/" || a.Docroot != "" {
		t.Errorf("root %q, docroot %q", a.Root, a.Docroot)
	}
	// WordPress 6.2 runs up to PHP 8.2.
	if a.PHPVersion != "8.2" {
		t.Errorf("php = %q, want 8.2", a.PHPVersion)
	}
	if a.Database != "mysql" || a.Dump == nil || a.Dump.Variant != "mysql" {
		t.Errorf("database %q, dump %+v", a.Database, a.Dump)
	}
	if a.Web != "apache" {
		t.Errorf("web = %q, want apache for a .htaccess site", a.Web)
	}
	if a.Config == nil || a.Config.Path != "wp-config.php" || a.Config.Mode != ConfigAdapt {
		t.Errorf("config = %+v", a.Config)
	}
	if !slices.Contains(a.PHPExtensions, "mysqli") {
		t.Errorf("extensions = %v", a.PHPExtensions)
	}
}

func TestAnalyzeLaravel(t *testing.T) {
	site := writeTarGz(t,
		member{name: "shop/artisan", body: "#!/usr/bin/env php"},
		member{name: "shop/composer.json", body: `{"require": {"php": "^8.1", "laravel/framework": "^10.0", "ext-redis": "*"}}`},
		member{name: "shop/.env", body: "APP_NAME=Shop\nDB_CONNECTION=pgsql\n"},
		member{name: "shop/public/index.php", body: "<?php"},
		member{name: "shop/public/.htaccess", body: "RewriteEngine On"},
		member{name: "shop/bootstrap/cache/config.php", body: "<?php return [];"},
	)
	a, err := Analyze(site, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Framework.ID != "laravel" || a.Docroot != "public" || a.Runtime != "php" {
		t.Errorf("analysis = %+v", a)
	}
	if a.PHPVersion != "8.5" {
		t.Errorf("php = %q", a.PHPVersion)
	}
	if a.Database != "postgresql" {
		t.Errorf("database = %q", a.Database)
	}
	if a.Web != "" {
		t.Errorf("web = %q; Laravel runs on every web server", a.Web)
	}
	if !slices.Contains(a.PHPExtensions, "redis") {
		t.Errorf("extensions = %v, want redis from ext-redis", a.PHPExtensions)
	}
	if a.Config == nil || a.Config.Mode != ConfigEnv {
		t.Errorf("config = %+v", a.Config)
	}
}

func TestAnalyzeComposerConstraintOutOfRange(t *testing.T) {
	site := writeZip(t,
		member{name: "composer.json", body: `{"require": {"php": ">=7.1 <7.3"}}`},
		member{name: "index.php", body: "<?php"},
	)
	a, err := Analyze(site, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.PHPVersion != "8.5" || !slices.ContainsFunc(a.Notices, func(n Notice) bool { return strings.Contains(n.Text, "does not offer") }) {
		t.Errorf("php %q, notices %+v", a.PHPVersion, a.Notices)
	}
}

func TestAnalyzePlainPHP(t *testing.T) {
	site := writeZip(t,
		member{name: "htdocs/index.php", body: "<?php include 'inc/config.php';"},
		member{name: "htdocs/inc/config.php", body: "<?php $db = mysqli_connect('localhost', 'u', 'p', 'd');"},
		member{name: "htdocs/old.php", body: "<?php $r = mysql_query('SELECT 1');"},
		member{name: "vendor/lib/x.php", body: "<?php mysql_connect();"},
		member{name: "README.txt", body: "hello"},
	)
	a, err := Analyze(site, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Framework.ID != "php" || a.Docroot != "htdocs" || a.Root != "" {
		t.Errorf("framework %q, docroot %q, root %q", a.Framework.ID, a.Docroot, a.Root)
	}
	if !slices.Equal(a.ConfigCandidates, []string{"htdocs/inc/config.php"}) {
		t.Errorf("candidates = %v", a.ConfigCandidates)
	}
	if a.Database != "mariadb" {
		t.Errorf("database = %q", a.Database)
	}
	// mysql_query needs a PHP Envoryx no longer has: the oldest one is suggested.
	if a.PHPVersion != "7.4" {
		t.Errorf("php = %q, want 7.4", a.PHPVersion)
	}
}

func TestAnalyzePHP7Code(t *testing.T) {
	site := writeZip(t, member{name: "index.php", body: "<?php $f = create_function('$a', 'return $a;'); foreach ($x as $y) {}"})
	a, err := Analyze(site, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.PHPVersion != "7.4" {
		t.Errorf("php = %q, want 7.4 for create_function", a.PHPVersion)
	}
}

func TestAnalyzeStatic(t *testing.T) {
	site := writeZip(t, member{name: "site/index.html", body: "<h1>Hi</h1>"}, member{name: "site/css/a.css", body: ""})
	a, err := Analyze(site, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Runtime != "static" || a.Docroot != "" || a.PHPVersion != "" || a.Database != "" {
		t.Errorf("analysis = %+v", a)
	}
}

func TestAnalyzeDrupalAndJoomla(t *testing.T) {
	drupal := writeZip(t,
		member{name: "composer.json", body: `{"require": {"drupal/core-recommended": "^10"}}`},
		member{name: "web/core/lib/Drupal.php", body: "<?php class Drupal { const VERSION = '10.1.5'; }"},
		member{name: "web/sites/default/settings.php", body: "<?php $databases['default']['default'] = ['driver' => 'mysql'];"},
		member{name: "web/index.php", body: "<?php"},
	)
	a, err := Analyze(drupal, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Framework.ID != "drupal" || a.Framework.Version != "10.1.5" || a.Docroot != "web" || a.Config == nil || a.Config.Path != "web/sites/default/settings.php" {
		t.Errorf("drupal = %+v config %+v", a.Framework, a.Config)
	}
	joomla := writeZip(t,
		member{name: "configuration.php", body: "<?php class JConfig { public $host = 'localhost'; }"},
		member{name: "libraries/src/Version.php", body: "<?php final class Version { public const MAJOR_VERSION = 3; public const MINOR_VERSION = 9; }"},
		member{name: "index.php", body: "<?php"},
	)
	a, err = Analyze(joomla, "", testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if a.Framework.ID != "joomla" || a.Framework.Version != "3.9" || a.PHPVersion != "7.4" {
		t.Errorf("joomla = %+v php %q", a.Framework, a.PHPVersion)
	}
}

func TestAnalyzeDump(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("-- MariaDB dump 10.19  Distrib 10.11.6-MariaDB\n-- Server version\t10.11.6-MariaDB-0+deb12u1\n"))
	_ = zw.Close()
	cases := map[string]struct {
		content string
		variant string
		err     bool
	}{
		"mariadb gz": {gz.String(), "mariadb", false},
		"phpmyadmin": {"-- phpMyAdmin SQL Dump\n-- Server-Version: 5.7.44\nCREATE TABLE `a` (x int);\n", "mariadb", false},
		"postgres":   {"--\n-- PostgreSQL database dump\n--\n-- Dumped from database version 15.4\n", "postgresql", false},
		"custom":     {"PGDMP\x01\x0e", "", true},
		"empty":      {"", "", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := AnalyzeDump(writeFile(t, "dump", c.content))
			if c.err {
				if !errors.Is(err, validate.ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %v", err)
				}
				return
			}
			if err != nil || d.Variant != c.variant {
				t.Fatalf("dump = %+v, %v", d, err)
			}
		})
	}
}

func TestOpenDumpForImportDropsServerStatements(t *testing.T) {
	mysql := "CREATE DATABASE /*!32312 IF NOT EXISTS*/ `old` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\nUSE `old`;\nCREATE TABLE t (x int);\nINSERT INTO t VALUES (1);\n"
	r, err := OpenDumpForImport(writeFile(t, "d.sql", mysql), "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if string(got) != "CREATE TABLE t (x int);\nINSERT INTO t VALUES (1);\n" {
		t.Errorf("mysql dump = %q", got)
	}

	pg := "CREATE DATABASE old WITH TEMPLATE = template0;\n\\connect old\nCREATE TABLE public.t (x text);\nALTER TABLE public.t OWNER TO olduser;\nCOPY public.t (x) FROM stdin;\nGRANT ALL\n\\.\nGRANT SELECT ON public.t TO reader;\n"
	r, err = OpenDumpForImport(writeFile(t, "d.sql", pg), "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(r)
	_ = r.Close()
	// Rows of a COPY block stay, whatever they look like.
	if string(got) != "CREATE TABLE public.t (x text);\nCOPY public.t (x) FROM stdin;\nGRANT ALL\n\\.\n" {
		t.Errorf("postgres dump = %q", got)
	}
}

func TestOpenDumpForImportLongLines(t *testing.T) {
	// The reader hands out 256 KiB at a time: the tail of a longer line starts a chunk
	// of its own and must not be taken for a statement.
	head := "INSERT INTO t VALUES ('"
	long := head + strings.Repeat("x", 256<<10-len(head)) + "USE `b`;');\nUSE `old`;\nSELECT 1;\n"
	r, err := OpenDumpForImport(writeFile(t, "d.sql", long), "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if want := strings.Replace(long, "USE `old`;\n", "", 1); string(got) != want {
		t.Errorf("got %d bytes ending %q, want %d bytes", len(got), got[max(0, len(got)-40):], len(want))
	}
}

func TestAdaptWordPress(t *testing.T) {
	dir := t.TempDir()
	orig := "<?php\ndefine( 'DB_NAME', 'old_db' );\ndefine('DB_USER', \"old\");\ndefine('DB_PASSWORD', 'p@ss);w');\ndefine('DB_HOST', 'mysql.example.com');\ndefine('WP_HOME', 'https://old.example.com');\n$table_prefix = 'wp_';\n/* That's all, stop editing! Happy publishing. */\nrequire_once ABSPATH . 'wp-settings.php';\n"
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	a := Analysis{Framework: Framework{ID: "wordpress"}, Config: &ConfigFile{Path: "wp-config.php", Mode: ConfigAdapt}}
	res, err := Adapt(dir, a, Database{Variant: "mariadb"}, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "wp-config.php"))
	s := string(got)
	for _, want := range []string{"define('DB_NAME', getenv('DB_DATABASE'));", "define('DB_HOST', getenv('DB_HOST') . ':' . getenv('DB_PORT'));", "define('WP_HOME', (", "$table_prefix = 'wp_';"} {
		if !strings.Contains(s, want) {
			t.Errorf("wp-config.php misses %q:\n%s", want, s)
		}
	}
	for _, gone := range []string{"old_db", "mysql.example.com", "old.example.com", "p@ss"} {
		if strings.Contains(s, gone) {
			t.Errorf("wp-config.php still holds %q", gone)
		}
	}
	if strings.Index(s, "WP_HOME") > strings.Index(s, "That's all") {
		t.Error("the address block must come before WordPress loads")
	}
	backup, _ := os.ReadFile(filepath.Join(dir, "wp-config.envoryx-original.php"))
	if !strings.HasPrefix(string(backup), originalGuard) || !strings.HasSuffix(string(backup), orig) {
		t.Errorf("original copy = %q", backup)
	}
	if !slices.Equal(res.Changed, []string{"wp-config.php"}) || !slices.Equal(res.Originals, []string{"wp-config.envoryx-original.php"}) {
		t.Errorf("result = %+v", res)
	}
	if info, _ := os.Stat(filepath.Join(dir, "wp-config.php")); info.Mode().Perm() != 0o640|0o200 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestAdaptJoomla(t *testing.T) {
	dir := t.TempDir()
	src := "<?php\nclass JConfig {\n\tpublic $dbtype = 'mysql';\n\tpublic $host = 'localhost';\n\tpublic $user = 'old';\n\tpublic $password = 'secret';\n\tpublic $db = 'olddb';\n\tpublic $live_site = 'https://old.example.com';\n\tpublic $log_path = '/home/old/public_html/administrator/logs';\n\tpublic $tmp_path = '/home/old/public_html/tmp';\n}\n"
	_ = os.WriteFile(filepath.Join(dir, "configuration.php"), []byte(src), 0o644)
	a := Analysis{Framework: Framework{ID: "joomla"}, Config: &ConfigFile{Path: "configuration.php", Mode: ConfigAdapt}}
	if _, err := Adapt(dir, a, Database{Variant: "mariadb", Host: "database", Port: 3306, Name: "shop", User: "shop", Password: "pw'1"}, -1, -1); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "configuration.php"))
	for _, want := range []string{"$dbtype = 'mysqli';", "$host = 'database:3306';", "$user = 'shop';", `$password = 'pw\'1';`, "$db = 'shop';", "$live_site = '';", "$log_path = __DIR__ . '/administrator/logs';", "$tmp_path = __DIR__ . '/tmp';"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("configuration.php misses %q:\n%s", want, got)
		}
	}
}

func TestAdaptDrupalTypo3Laravel(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "web/sites/default"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "web/sites/default/settings.php"), []byte("<?php\n$databases = [];\n"), 0o444)
	_ = os.Chmod(filepath.Join(dir, "web/sites/default"), 0o555)
	defer os.Chmod(filepath.Join(dir, "web/sites/default"), 0o755)
	a := Analysis{Framework: Framework{ID: "drupal"}, Config: &ConfigFile{Path: "web/sites/default/settings.php", Mode: ConfigAdapt}}
	if _, err := Adapt(dir, a, Database{Variant: "postgresql"}, -1, -1); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "web/sites/default/settings.php"))
	if !strings.Contains(string(got), "'driver' => 'pgsql'") || !strings.Contains(string(got), "getenv('DB_DATABASE')") {
		t.Errorf("settings.php = %s", got)
	}
	if info, _ := os.Stat(filepath.Join(dir, "web/sites/default")); info.Mode().Perm() != 0o555 {
		t.Errorf("sites/default must be read-only again, is %v", info.Mode().Perm())
	}

	dir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "typo3conf"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "typo3conf/LocalConfiguration.php"), []byte("<?php return [];"), 0o644)
	a = Analysis{Framework: Framework{ID: "typo3"}, Config: &ConfigFile{Path: "typo3conf/LocalConfiguration.php", Mode: ConfigAdapt}}
	res, err := Adapt(dir, a, Database{Variant: "mariadb"}, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "typo3conf/AdditionalConfiguration.php"))
	if !strings.HasPrefix(string(got), "<?php") || !strings.Contains(string(got), "'driver' => 'mysqli'") || len(res.Originals) != 0 {
		t.Errorf("AdditionalConfiguration.php = %s, result %+v", got, res)
	}

	dir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "bootstrap/cache"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bootstrap/cache/config.php"), []byte("<?php return [];"), 0o644)
	res, err = Adapt(dir, Analysis{Framework: Framework{ID: "laravel"}}, Database{}, -1, -1)
	if err != nil || !slices.Equal(res.Removed, []string{"bootstrap/cache/config.php"}) {
		t.Errorf("laravel: %+v, %v", res, err)
	}
}

func TestAdaptRefusesSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.php")
	_ = os.WriteFile(outside, []byte("<?php define('DB_NAME', 'x');"), 0o644)
	_ = os.Symlink(outside, filepath.Join(dir, "wp-config.php"))
	a := Analysis{Framework: Framework{ID: "wordpress"}, Config: &ConfigFile{Path: "wp-config.php", Mode: ConfigAdapt}}
	if _, err := Adapt(dir, a, Database{}, -1, -1); err == nil {
		t.Fatal("a symlinked wp-config.php must not be written")
	}
	if b, _ := os.ReadFile(outside); string(b) != "<?php define('DB_NAME', 'x');" {
		t.Error("the file outside the project changed")
	}
}

func TestStaging(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	st := Staging{Dir: t.TempDir(), Now: func() time.Time { return now }}
	up, err := st.Begin()
	if err != nil {
		t.Fatal(err)
	}
	site, _ := os.ReadFile(writeZip(t, member{name: "index.html", body: "hi"}))
	if err := up.Site(bytes.NewReader(site), "../../My Site.zip"); err != nil {
		t.Fatal(err)
	}
	staged, err := up.Finish(testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if staged.SiteName != "My Site.zip" || staged.DumpFile() != "" {
		t.Errorf("staged = %+v", staged)
	}
	got, err := st.Get(staged.ID)
	if err != nil || got.Analysis.Runtime != "static" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	now = now.Add(TTL + time.Minute)
	if _, err := st.Get(staged.ID); err == nil {
		t.Error("an expired upload must be gone")
	}
	if _, err := st.Get("../x"); err == nil {
		t.Error("ids are UUIDs")
	}

	// An upload that is no archive is refused and removed.
	up, _ = st.Begin()
	_ = up.Site(strings.NewReader("hello"), "x.zip")
	if _, err := up.Finish(testOptions); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if entries, _ := os.ReadDir(st.Dir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}
