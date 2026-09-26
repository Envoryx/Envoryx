package siteimport

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Framework is what the site was recognised as.
type Framework struct {
	// ID is wordpress, laravel, symfony, drupal, typo3, joomla, shopware, craft, composer
	// (another Composer application), php (plain PHP), static, node or python.
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Config modes: how the site learns about the project database.
const (
	// ConfigAdapt: Envoryx can rewrite the file (WordPress, Drupal, TYPO3, Joomla).
	ConfigAdapt = "adapt"
	// ConfigEnv: the variables Envoryx injects override the file (.env of Laravel, Symfony).
	ConfigEnv = "env"
	// ConfigManual: the file has to be edited by hand.
	ConfigManual = "manual"
)

// ConfigFile is the file holding the site's database connection.
type ConfigFile struct {
	Path string `json:"path"` // relative to the project directory
	Mode string `json:"mode"`
}

// Notice is something the import wants to tell. Text is English with {{placeholders}}
// filled from Params – the web UI translates it like the progress messages.
type Notice struct {
	Level  string            `json:"level"` // info | warning
	Text   string            `json:"text"`
	Params map[string]string `json:"params,omitempty"`
}

// Analysis is what an uploaded site looks like and how Envoryx suggests to run it.
type Analysis struct {
	Format Format `json:"format"`
	// Root is the folder the archive wraps the site in ("public_html/"); it is left out
	// when the files are unpacked.
	Root  string `json:"root,omitempty"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`

	Framework Framework `json:"framework"`
	// Runtime is php, static, node, python or go.
	Runtime       string   `json:"runtime"`
	PHPVersion    string   `json:"phpVersion,omitempty"`
	PHPExtensions []string `json:"phpExtensions,omitempty"`
	Docroot       string   `json:"docroot"`
	// Web is the suggested web server ("apache" for sites that rely on .htaccess, ""
	// for the default).
	Web string `json:"web,omitempty"`
	// Database is the suggested database (mariadb, mysql, postgresql; "" = none).
	Database string      `json:"database,omitempty"`
	Config   *ConfigFile `json:"config,omitempty"`
	// ConfigCandidates are PHP files of a plain site that open a database connection.
	ConfigCandidates []string  `json:"configCandidates,omitempty"`
	Dump             *DumpInfo `json:"dump,omitempty"`
	Notices          []Notice  `json:"notices"`
}

// Options carries what the analysis cannot know by itself.
type Options struct {
	// PHPVersions are the PHP versions Envoryx offers ("8.5", "8.4" … "7.4"), newest
	// first, without previews.
	PHPVersions []string
	// DefaultPHP is the version a new project gets.
	DefaultPHP string
}

// Files kept in memory while walking, by base name (small ones only).
var interesting = map[string]bool{
	"composer.json": true, "package.json": true, ".env": true, ".htaccess": true,
	"wp-config.php": true, "version.php": true, "settings.php": true, "configuration.php": true,
	"Drupal.php": true, "bootstrap.inc": true, "Typo3Version.php": true, "Version.php": true,
	"LocalConfiguration.php": true, "requirements.txt": true, "pyproject.toml": true,
}

// Directories whose PHP files are libraries or CMS cores, not the site's own code.
var libraryDirs = []string{"vendor/", "node_modules/", "wp-includes/", "wp-admin/", "core/", "typo3/", "libraries/", "cache/", "var/cache/", "storage/framework/"}

var (
	dbCallRe = regexp.MustCompile(`\b(mysqli_connect|mysql_p?connect|pg_p?connect)\s*\(|new\s+(mysqli|PDO)\s*\(`)
	// Removed in PHP 7: the old MySQL extension and POSIX regular expressions.
	php5Re = regexp.MustCompile(`\b(mysql_(p?connect|query|select_db|fetch_array|fetch_assoc)|eregi?(_replace)?)\s*\(`)
	// Removed in PHP 8.
	php7Re        = regexp.MustCompile(`\b(create_function|each)\s*\(`)
	wpVersionRe   = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)
	drupalRe      = regexp.MustCompile(`const VERSION = '([^']+)'`)
	drupal7Re     = regexp.MustCompile(`define\('VERSION', '([^']+)'\)`)
	typo3Re       = regexp.MustCompile(`VERSION = '([^']+)'`)
	joomlaMajorRe = regexp.MustCompile(`MAJOR_VERSION\s*=\s*(\d+)`)
	joomlaMinorRe = regexp.MustCompile(`MINOR_VERSION\s*=\s*(\d+)`)
	joomlaRelRe   = regexp.MustCompile(`RELEASE\s*=\s*'([^']+)'`)
	envLineRe     = regexp.MustCompile(`(?m)^\s*(?:export\s+)?([A-Z_][A-Z0-9_]*)\s*=\s*["']?([^"'\r\n#]*)`)
	pgDriverRe    = regexp.MustCompile(`'driver'\s*=>\s*'(pgsql|pdo_pgsql)'`)
	trustedHostRe = regexp.MustCompile(`(?m)^\s*\$settings\['trusted_host_patterns'\]`)
)

// scan is what one walk over the archive collects.
type scan struct {
	names    []string
	files    map[string]int64
	dirs     map[string]bool
	contents map[string][]byte
	dbCalls  []string // PHP files opening a database connection
	pgCalls  bool     // … one of them with pg_connect
	php5     []string // PHP files calling functions PHP 7 removed
	php7     []string // … functions PHP 8 removed
	phpFiles int
	bytes    int64
}

func isLibrary(name string) bool {
	for _, d := range libraryDirs {
		if strings.HasPrefix(name, d) || strings.Contains(name, "/"+d) {
			return true
		}
	}
	return false
}

func collect(file string, format Format) (*scan, error) {
	s := &scan{files: map[string]int64{}, dirs: map[string]bool{}, contents: map[string][]byte{}}
	err := walk(file, format, func(e entry) error {
		s.names = append(s.names, e.Name)
		switch e.Kind {
		case entryDir:
			s.dirs[e.Name] = true
			return nil
		case entryFile:
		default:
			return nil
		}
		s.files[e.Name] = e.Size
		s.bytes += e.Size
		// Parent directories are not always archive members of their own.
		for d := path.Dir(e.Name); d != "."; d = path.Dir(d) {
			if s.dirs[d] {
				break
			}
			s.dirs[d] = true
		}
		base := path.Base(e.Name)
		ext := path.Ext(base)
		isPHP := ext == ".php" || ext == ".inc" || ext == ".phtml"
		if isPHP {
			s.phpFiles++
		}
		wanted := interesting[base] && e.Size <= 1<<20 && strings.Count(e.Name, "/") <= 8 && !strings.Contains(e.Name, "node_modules/")
		// Composer and CMS core files hold the versions, so vendor/ may be read for those.
		if wanted && strings.Contains(e.Name, "vendor/") && base != "Version.php" {
			wanted = false
		}
		scanPHP := isPHP && e.Size <= 256<<10 && !isLibrary(e.Name) && s.phpFiles <= 20000
		if !wanted && !scanPHP {
			return nil
		}
		r, err := e.open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(r, 1<<20))
		if err != nil {
			return err
		}
		if wanted {
			s.contents[e.Name] = data
		}
		if scanPHP {
			if m := dbCallRe.FindSubmatch(data); m != nil {
				s.dbCalls = append(s.dbCalls, e.Name)
				s.pgCalls = s.pgCalls || strings.HasPrefix(string(m[1]), "pg_")
			}
			if php5Re.Match(data) {
				s.php5 = append(s.php5, e.Name)
			}
			if php7Re.Match(data) {
				s.php7 = append(s.php7, e.Name)
			}
		}
		return nil
	})
	return s, err
}

// site answers questions about the files relative to the stripped root.
type site struct {
	*scan
	root string
}

func (s site) has(rel string) bool     { _, ok := s.files[s.root+rel]; return ok }
func (s site) hasDir(rel string) bool  { return s.dirs[strings.TrimSuffix(s.root+rel, "/")] }
func (s site) read(rel string) []byte  { return s.contents[s.root+rel] }
func (s site) text(rel string) string  { return string(s.read(rel)) }
func (s site) rel(name string) string  { return strings.TrimPrefix(name, s.root) }
func (s site) under(dir string) string { return strings.TrimSuffix(path.Join(dir, "x"), "x") }

type composerJSON struct {
	Name    string            `json:"name"`
	Require map[string]string `json:"require"`
	Config  struct {
		Platform map[string]string `json:"platform"`
	} `json:"config"`
	Extra struct {
		DrupalScaffold struct {
			Locations map[string]string `json:"locations"`
		} `json:"drupal-scaffold"`
		Typo3CMS struct {
			WebDir string `json:"web-dir"`
		} `json:"typo3/cms"`
	} `json:"extra"`
}

func (s site) composer() *composerJSON {
	data := s.read("composer.json")
	if data == nil {
		return nil
	}
	var c composerJSON
	if json.Unmarshal(data, &c) != nil {
		return nil
	}
	return &c
}

func (c *composerJSON) requires(pkg string) bool {
	if c == nil {
		return false
	}
	_, ok := c.Require[pkg]
	return ok
}

// Analyze inspects an uploaded site archive (and dump, "" = none).
func Analyze(siteFile, dumpFile string, opt Options) (Analysis, error) {
	format, err := sniffFormat(siteFile)
	if err != nil {
		return Analysis{}, err
	}
	sc, err := collect(siteFile, format)
	if err != nil {
		return Analysis{}, err
	}
	if len(sc.files) == 0 {
		return Analysis{}, fmt.Errorf("the archive holds no files")
	}
	s := site{scan: sc, root: commonRoot(sc.names)}
	a := Analysis{Format: format, Root: s.root, Files: len(sc.files), Bytes: sc.bytes, Notices: []Notice{}}
	if s.root != "" {
		a.notice("info", "The archive's folder {{folder}} became the project directory.", "folder", strings.TrimSuffix(s.root, "/"))
	}
	if dumpFile != "" {
		d, err := AnalyzeDump(dumpFile)
		if err != nil {
			return Analysis{}, err
		}
		a.Dump = &d
	}
	comp := s.composer()
	a.detect(s, comp)
	a.choosePHP(s, comp, opt)
	a.chooseDatabase(s)
	if a.Runtime == "php" || a.Runtime == "static" {
		a.chooseWeb(s)
	}
	return a, nil
}

func (a *Analysis) notice(level, text string, kv ...string) {
	n := Notice{Level: level, Text: text}
	if len(kv) > 0 {
		n.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			n.Params[kv[i]] = kv[i+1]
		}
	}
	a.Notices = append(a.Notices, n)
}

func (a *Analysis) addExtension(names ...string) {
	for _, n := range names {
		if !slices.Contains(a.PHPExtensions, n) {
			a.PHPExtensions = append(a.PHPExtensions, n)
		}
	}
}

// firstDir returns the first of the directories (relative, "" = the root) that holds
// the file.
func (s site) firstDir(file string, dirs ...string) (string, bool) {
	for _, d := range dirs {
		if s.has(s.under(d) + file) {
			return d, true
		}
	}
	return "", false
}

var docrootGuesses = []string{"", "public", "web", "htdocs", "httpdocs", "public_html", "www", "html"}

func (a *Analysis) detect(s site, comp *composerJSON) {
	a.Runtime = "php"
	switch {
	case s.has("wp-config.php") || s.has("wp-includes/version.php"):
		a.Framework = Framework{ID: "wordpress", Name: "WordPress"}
		if m := wpVersionRe.FindStringSubmatch(s.text("wp-includes/version.php")); m != nil {
			a.Framework.Version = m[1]
		}
		a.addExtension("mysqli")
		if s.has("wp-config.php") {
			a.Config = &ConfigFile{Path: "wp-config.php", Mode: ConfigAdapt}
		} else {
			a.notice("warning", "The archive has no wp-config.php; WordPress will ask for the database connection when the site is opened.")
		}
		a.notice("info", "Links stored in the database still point to the old address. Replace them with a search-and-replace plugin or “wp search-replace”.")

	case s.has("artisan") && comp.requires("laravel/framework"):
		a.Framework = Framework{ID: "laravel", Name: "Laravel"}
		a.Docroot = "public"
		a.Config = &ConfigFile{Path: ".env", Mode: ConfigEnv}
		if s.has("bootstrap/cache/config.php") {
			a.notice("warning", "bootstrap/cache/config.php holds the configuration of the old server; leave “Clear the cached configuration” on, or run “artisan config:clear”.")
		}

	case comp.requires("shopware/core") || comp.requires("shopware/platform"):
		a.Framework = Framework{ID: "shopware", Name: "Shopware"}
		a.Docroot = "public"
		a.Config = &ConfigFile{Path: ".env", Mode: ConfigEnv}

	case comp.requires("craftcms/cms"):
		a.Framework = Framework{ID: "craft", Name: "Craft CMS"}
		a.Docroot = "web"
		a.Config = &ConfigFile{Path: ".env", Mode: ConfigManual}

	case s.has("bin/console") && (comp.requires("symfony/framework-bundle") || comp.requires("symfony/symfony")):
		a.Framework = Framework{ID: "symfony", Name: "Symfony"}
		a.Docroot = "public"
		if !s.hasDir("public") && s.hasDir("web") {
			a.Docroot = "web" // Symfony 2 and 3
		}
		a.Config = &ConfigFile{Path: ".env", Mode: ConfigEnv}
		if !s.has(".env") {
			a.Config = &ConfigFile{Path: "app/config/parameters.yml", Mode: ConfigManual}
		}

	case s.has("core/lib/Drupal.php") || s.has("web/core/lib/Drupal.php") || s.has("docroot/core/lib/Drupal.php") || s.has("includes/bootstrap.inc") && s.has("modules/system/system.module"):
		a.Framework = Framework{ID: "drupal", Name: "Drupal"}
		if d, ok := s.firstDir("core/lib/Drupal.php", "", "web", "docroot"); ok {
			a.Docroot = d
			if m := drupalRe.FindStringSubmatch(s.text(s.under(d) + "core/lib/Drupal.php")); m != nil {
				a.Framework.Version = m[1]
			}
		} else if m := drupal7Re.FindStringSubmatch(s.text("includes/bootstrap.inc")); m != nil {
			a.Framework.Version = m[1]
		}
		settings := s.under(a.Docroot) + "sites/default/settings.php"
		if s.has(settings) {
			a.Config = &ConfigFile{Path: settings, Mode: ConfigAdapt}
			if trustedHostRe.MatchString(s.text(settings)) {
				a.notice("warning", "settings.php limits the host names Drupal answers to (trusted_host_patterns); add the project's domain there.")
			}
		}

	case s.hasDir("typo3/sysext") || s.hasDir("public/typo3") || comp.requires("typo3/cms-core"):
		a.Framework = Framework{ID: "typo3", Name: "TYPO3"}
		if s.hasDir("public") && (comp.requires("typo3/cms-core") || s.hasDir("public/typo3")) {
			a.Docroot = "public"
		}
		for _, p := range []string{"typo3/sysext/core/Classes/Information/Typo3Version.php", "vendor/typo3/cms-core/Classes/Information/Typo3Version.php", s.under(a.Docroot) + "typo3/sysext/core/Classes/Information/Typo3Version.php"} {
			if m := typo3Re.FindStringSubmatch(s.text(p)); m != nil {
				a.Framework.Version = m[1]
				break
			}
		}
		for _, p := range []string{"config/system/settings.php", s.under(a.Docroot) + "typo3conf/LocalConfiguration.php", "typo3conf/LocalConfiguration.php"} {
			if s.has(p) {
				a.Config = &ConfigFile{Path: p, Mode: ConfigAdapt}
				break
			}
		}
		a.addExtension("mysqli")

	case s.has("configuration.php") && strings.Contains(s.text("configuration.php"), "JConfig"):
		a.Framework = Framework{ID: "joomla", Name: "Joomla"}
		v := s.text("libraries/src/Version.php")
		if maj, minor := joomlaMajorRe.FindStringSubmatch(v), joomlaMinorRe.FindStringSubmatch(v); maj != nil && minor != nil {
			a.Framework.Version = maj[1] + "." + minor[1]
		} else if m := joomlaRelRe.FindStringSubmatch(s.text("libraries/cms/version/version.php")); m != nil {
			a.Framework.Version = m[1]
		}
		a.Config = &ConfigFile{Path: "configuration.php", Mode: ConfigAdapt}
		a.addExtension("mysqli")

	case comp != nil:
		a.Framework = Framework{ID: "composer", Name: "PHP (Composer)"}
		if comp.Name != "" {
			a.Framework.Name = comp.Name
		}
		a.Docroot, _ = s.firstDir("index.php", docrootGuesses...)
		a.Config = s.envConfig()

	case s.phpFiles > 0:
		a.Framework = Framework{ID: "php", Name: "PHP"}
		a.Docroot, _ = s.firstDir("index.php", docrootGuesses...)
		if a.Docroot == "" {
			a.Docroot, _ = s.firstDir("index.html", docrootGuesses...)
		}
		a.plainPHPConfig(s)

	case s.has("go.mod"):
		// Before package.json: a Go server with a frontend toolchain is a Go project.
		a.Framework = Framework{ID: "go", Name: "Go"}
		a.Runtime = "go"
		return

	case s.has("package.json"):
		a.Framework = Framework{ID: "node", Name: "Node.js"}
		a.Runtime = "node"
		if !s.hasDir("node_modules") {
			a.notice("info", "node_modules/ is not part of the archive: run “npm install” from the Actions tab after creation.")
		}
		return

	case s.has("manage.py") || s.has("requirements.txt") || s.has("pyproject.toml"):
		a.Framework = Framework{ID: "python", Name: "Python"}
		if s.has("manage.py") {
			a.Framework.Name = "Django"
		}
		a.Runtime = "python"
		return

	default:
		a.Framework = Framework{ID: "static", Name: "Static site"}
		a.Runtime = "static"
		a.Docroot, _ = s.firstDir("index.html", docrootGuesses...)
		return
	}
	if comp != nil && !s.hasDir("vendor") {
		a.notice("info", "vendor/ is not part of the archive: run “composer install” from the Actions tab after creation.")
	}
	// Extensions the application asks for itself (composer.json "ext-…").
	if comp != nil {
		known := []string{"mysqli", "pdo_mysql", "pdo_pgsql", "gd", "intl", "zip", "bcmath", "imagick", "redis", "memcached", "amqp", "mongodb"}
		for req := range comp.Require {
			if ext, ok := strings.CutPrefix(req, "ext-"); ok && slices.Contains(known, ext) {
				a.addExtension(ext)
			}
		}
	}
	sort.Strings(a.PHPExtensions)
}

// envConfig points at the .env of a Composer application, if it has one.
func (s site) envConfig() *ConfigFile {
	if s.has(".env") {
		return &ConfigFile{Path: ".env", Mode: ConfigManual}
	}
	return nil
}

// plainPHPConfig lists the files of a hand-written site that open a connection.
func (a *Analysis) plainPHPConfig(s site) {
	calls := slices.Clone(s.dbCalls)
	sort.Slice(calls, func(i, j int) bool {
		di, dj := strings.Count(calls[i], "/"), strings.Count(calls[j], "/")
		if di != dj {
			return di < dj
		}
		return calls[i] < calls[j]
	})
	for _, c := range calls {
		if len(a.ConfigCandidates) == 5 {
			break
		}
		a.ConfigCandidates = append(a.ConfigCandidates, s.rel(c))
	}
	if len(a.ConfigCandidates) > 0 {
		a.Config = &ConfigFile{Path: a.ConfigCandidates[0], Mode: ConfigManual}
		if !s.pgCalls {
			a.addExtension("mysqli")
		}
	}
}

// choosePHP picks the newest offered PHP version the site can run on.
func (a *Analysis) choosePHP(s site, comp *composerJSON, opt Options) {
	if a.Runtime != "php" {
		return
	}
	ok := func(constraint string) []string {
		var out []string
		for _, v := range opt.PHPVersions {
			parsed, _, good := parseVersion(v)
			if good && matches(constraint, parsed[0], parsed[1]) {
				out = append(out, v)
			}
		}
		return out
	}
	candidates := slices.Clone(opt.PHPVersions)
	if comp != nil {
		constraint := comp.Config.Platform["php"]
		if constraint == "" {
			constraint = comp.Require["php"]
		}
		if constraint != "" {
			if fit := ok(constraint); len(fit) > 0 {
				candidates = fit
			} else {
				a.notice("warning", "composer.json asks for PHP {{constraint}}, which Envoryx does not offer.", "constraint", constraint)
			}
		}
	}
	// Old CMS releases run on old PHP only.
	if limit := frameworkPHPLimit(a.Framework); limit != "" {
		if fit := ok("<=" + limit); len(fit) > 0 {
			candidates = intersect(candidates, fit)
		}
	}
	generic := a.Framework.ID == "php" || a.Framework.ID == "composer"
	if generic && len(s.php5) > 0 {
		a.notice("warning", "The site calls functions PHP 7 removed (mysql_*, ereg) in {{file}} and more. Envoryx's oldest PHP is {{version}}; the code has to be updated before it runs.",
			"file", s.rel(s.php5[0]), "version", oldest(opt.PHPVersions))
		candidates = []string{oldest(opt.PHPVersions)}
	} else if generic && len(s.php7) > 0 {
		if fit := ok("<8"); len(fit) > 0 {
			candidates = intersect(candidates, fit)
			a.notice("info", "The code uses functions PHP 8 removed (create_function, each) in {{file}}, so PHP {{version}} is suggested.", "file", s.rel(s.php7[0]), "version", fit[0])
		}
	}
	if len(candidates) == 0 {
		candidates = []string{opt.DefaultPHP}
	}
	// The default is preferred over newer versions when it fits: it is what Envoryx
	// tests best.
	a.PHPVersion = candidates[0]
	if slices.Contains(candidates, opt.DefaultPHP) {
		a.PHPVersion = opt.DefaultPHP
	}
}

func intersect(a, b []string) []string {
	var out []string
	for _, v := range a {
		if slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return a
	}
	return out
}

func oldest(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[len(versions)-1]
}

// frameworkPHPLimit is the newest PHP an old CMS release runs on ("" = any).
func frameworkPHPLimit(f Framework) string {
	v, parts, ok := parseVersion(f.Version)
	if !ok || parts == 0 {
		return ""
	}
	below := func(maj, min int) bool { return v[0] < maj || v[0] == maj && v[1] < min }
	switch f.ID {
	case "wordpress":
		switch {
		case below(5, 6):
			return "7.4"
		case below(6, 1):
			return "8.0"
		case below(6, 4):
			return "8.2"
		}
	case "drupal":
		switch {
		case v[0] <= 7:
			return "8.1"
		case v[0] == 8 || below(9, 3):
			return "7.4"
		case v[0] == 9:
			return "8.1"
		}
	case "joomla":
		switch {
		case below(3, 10):
			return "7.4"
		case v[0] == 3:
			return "8.0"
		case v[0] == 4:
			return "8.2"
		}
	case "typo3":
		switch {
		case v[0] <= 9:
			return "7.4"
		case v[0] == 10:
			return "7.4"
		case v[0] == 11:
			return "8.1"
		}
	}
	return ""
}

// chooseDatabase suggests the database: the dump's origin when there is one, otherwise
// what the configuration names.
func (a *Analysis) chooseDatabase(s site) {
	configured := ""
	switch a.Framework.ID {
	case "wordpress", "joomla":
		configured = "mariadb"
	case "typo3", "drupal":
		configured = "mariadb"
		if a.Config != nil && pgDriverRe.MatchString(s.text(a.Config.Path)) {
			configured = "postgresql"
		}
	case "laravel", "symfony", "shopware", "craft", "composer":
		configured = envDatabase(s.text(".env"))
	case "php":
		if len(s.dbCalls) > 0 {
			configured = "mariadb"
			if s.pgCalls {
				configured = "postgresql"
			}
		}
	}
	if a.Dump == nil {
		a.Database = configured
		if configured != "" && a.Framework.ID != "php" {
			a.notice("info", "No database dump was uploaded: the project database starts empty.")
		}
		return
	}
	a.Database = a.Dump.Variant
	if a.Database == "" {
		a.Database = configured
	}
	if a.Database == "" {
		a.Database = "mariadb"
	}
	family := func(v string) string {
		if v == "mysql" {
			return "mariadb"
		}
		return v
	}
	if configured != "" && a.Dump.Variant != "" && family(configured) != family(a.Dump.Variant) {
		a.notice("warning", "The dump comes from {{dump}}, but the site's configuration names {{config}}.", "dump", a.Dump.Variant, "config", configured)
	}
	if a.Database == "postgresql" {
		a.addExtension("pdo_pgsql")
		sort.Strings(a.PHPExtensions)
	}
}

// envDatabase reads the database a .env names (DB_CONNECTION or DATABASE_URL).
func envDatabase(env string) string {
	vals := map[string]string{}
	for _, m := range envLineRe.FindAllStringSubmatch(env, -1) {
		vals[m[1]] = strings.TrimSpace(m[2])
	}
	pick := func(v string) string {
		v = strings.ToLower(v)
		switch {
		case strings.HasPrefix(v, "pgsql"), strings.HasPrefix(v, "postgres"):
			return "postgresql"
		case strings.HasPrefix(v, "mariadb"), strings.HasPrefix(v, "mysql"):
			return "mariadb"
		}
		return ""
	}
	if d := pick(vals["DB_CONNECTION"]); d != "" {
		return d
	}
	if u := vals["DATABASE_URL"]; u != "" {
		return pick(u)
	}
	if vals["CRAFT_DB_DRIVER"] != "" {
		return pick(vals["CRAFT_DB_DRIVER"])
	}
	return ""
}

// chooseWeb suggests Apache for sites whose .htaccess does more than an application's
// front controller needs: the other web servers ignore it.
func (a *Analysis) chooseWeb(s site) {
	switch a.Framework.ID {
	case "laravel", "symfony", "shopware", "craft":
		return
	}
	if s.has(s.under(a.Docroot) + ".htaccess") {
		a.Web = "apache"
		a.notice("info", "The site brings a .htaccess file, so Apache is suggested: it is the only web server that reads it.")
	}
}
