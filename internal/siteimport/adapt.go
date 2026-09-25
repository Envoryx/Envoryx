package siteimport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Database is the project database a site is wired to.
type Database struct {
	Variant  string // mariadb, mysql, postgresql
	Host     string
	Port     int
	Name     string
	User     string
	Password string
}

// Adapted lists what Adapt changed, relative to the project directory.
type Adapted struct {
	Changed []string `json:"changed"`
	// Originals are the untouched copies of changed files. They keep the .php
	// extension and start with a guard that answers 404, so the web server never hands
	// out the old credentials as text.
	Originals []string `json:"originals"`
	Removed   []string `json:"removed"`
}

const originalGuard = "<?php http_response_code(404); exit; /* Copy of the original, kept by the Envoryx import. */ ?>\n"

// Adapt wires the site's own configuration to the project database. Settings are read
// from the variables Envoryx injects (DB_HOST, DB_DATABASE…) wherever the file is PHP
// code; Joomla's configuration class cannot call functions, so db gives it the values.
// Caches that hold the old configuration are removed. uid:gid own what is written when
// running as root.
func Adapt(dir string, a Analysis, db Database, uid, gid int) (Adapted, error) {
	var out Adapted
	w := writer{dir: dir, uid: uid, gid: gid, out: &out}
	switch a.Framework.ID {
	case "wordpress":
		if a.Config != nil && a.Config.Mode == ConfigAdapt {
			if err := w.edit(a.Config.Path, adaptWordPress); err != nil {
				return out, err
			}
		}
	case "drupal":
		if a.Config != nil {
			driver := "mysql"
			if db.Variant == "postgresql" {
				driver = "pgsql"
			}
			if err := w.edit(a.Config.Path, func(src string) (string, error) {
				return appendPHP(src, drupalBlock(driver)), nil
			}); err != nil {
				return out, err
			}
		}
	case "typo3":
		if a.Config != nil {
			driver := "mysqli"
			if db.Variant == "postgresql" {
				driver = "pdo_pgsql"
			}
			target := path.Join(path.Dir(a.Config.Path), "additional.php")
			if path.Base(a.Config.Path) == "LocalConfiguration.php" {
				target = path.Join(path.Dir(a.Config.Path), "AdditionalConfiguration.php")
			}
			if err := w.edit(target, func(src string) (string, error) {
				if src == "" {
					src = "<?php\n"
				}
				return appendPHP(src, typo3Block(driver)), nil
			}); err != nil {
				return out, err
			}
		}
	case "joomla":
		if a.Config != nil {
			if err := w.edit(a.Config.Path, func(src string) (string, error) { return adaptJoomla(src, db), nil }); err != nil {
				return out, err
			}
		}
	case "laravel":
		if err := w.remove("bootstrap/cache/config.php"); err != nil {
			return out, err
		}
	case "symfony", "shopware":
		if err := w.remove("var/cache"); err != nil {
			return out, err
		}
	}
	return out, nil
}

type writer struct {
	dir      string
	uid, gid int
	out      *Adapted
}

func (w writer) resolve(rel string) (string, error) {
	p := filepath.Join(w.dir, filepath.FromSlash(rel))
	if r, err := filepath.Rel(w.dir, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project", rel)
	}
	// A symlink in the site must not make Envoryx write somewhere else.
	if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		root, _ := filepath.EvalSymlinks(w.dir)
		if r, err := filepath.Rel(root, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s is outside the project", rel)
		}
	}
	if info, err := os.Lstat(p); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symbolic link; it was left alone", rel)
	}
	return p, nil
}

// edit rewrites a file (created when missing), keeping a guarded copy of the original.
func (w writer) edit(rel string, fn func(string) (string, error)) error {
	p, err := w.resolve(rel)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(p)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next, err := fn(string(src))
	if err != nil || next == string(src) {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(p); err == nil {
		mode = info.Mode().Perm()
	}
	// Drupal keeps sites/default read-only; write access is lent for the change only.
	dir := filepath.Dir(p)
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o200 == 0 {
		_ = os.Chmod(dir, info.Mode().Perm()|0o200)
		defer os.Chmod(dir, info.Mode().Perm())
	}
	if exists {
		ext := path.Ext(rel)
		orig := strings.TrimSuffix(rel, ext) + ".envoryx-original" + ext
		op, err := w.resolve(orig)
		if err != nil {
			return err
		}
		if err := w.write(op, originalGuard+string(src), 0o600); err != nil {
			return err
		}
		w.out.Originals = append(w.out.Originals, orig)
		if mode&0o200 == 0 {
			_ = os.Chmod(p, mode|0o200)
		}
	}
	if err := w.write(p, next, mode|0o200); err != nil {
		return err
	}
	if exists && mode&0o200 == 0 {
		_ = os.Chmod(p, mode)
	}
	w.out.Changed = append(w.out.Changed, rel)
	return nil
}

func (w writer) write(p, content string, mode fs.FileMode) error {
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		_ = os.Lchown(p, w.uid, w.gid)
	}
	return nil
}

func (w writer) remove(rel string) error {
	p, err := w.resolve(rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err != nil {
		return nil
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	w.out.Removed = append(w.out.Removed, rel)
	return nil
}

// appendPHP adds code at the end of a PHP file, reopening PHP if the file closes it.
func appendPHP(src, code string) string {
	trimmed := strings.TrimRight(src, " \t\r\n")
	if strings.HasSuffix(trimmed, "?>") {
		return trimmed + "\n<?php\n" + code
	}
	return trimmed + "\n\n" + code
}

func drupalBlock(driver string) string {
	return `// Added by Envoryx: the project database (Envoryx injects DB_*).
$databases['default']['default'] = array_merge($databases['default']['default'] ?? [], [
  'driver' => '` + driver + `',
  'database' => getenv('DB_DATABASE'),
  'username' => getenv('DB_USERNAME'),
  'password' => getenv('DB_PASSWORD'),
  'host' => getenv('DB_HOST'),
  'port' => getenv('DB_PORT'),
]);
`
}

func typo3Block(driver string) string {
	return `// Added by Envoryx: the project database (Envoryx injects DB_*).
$GLOBALS['TYPO3_CONF_VARS']['DB']['Connections']['Default'] = array_merge($GLOBALS['TYPO3_CONF_VARS']['DB']['Connections']['Default'] ?? [], [
    'driver' => '` + driver + `',
    'dbname' => getenv('DB_DATABASE'),
    'user' => getenv('DB_USERNAME'),
    'password' => getenv('DB_PASSWORD'),
    'host' => getenv('DB_HOST'),
    'port' => (int) getenv('DB_PORT'),
]);
`
}

var (
	wpDefineRe = regexp.MustCompile(`(?m)^[ \t]*define\s*\(\s*['"](DB_NAME|DB_USER|DB_PASSWORD|DB_HOST|WP_HOME|WP_SITEURL)['"]\s*,.*?\)\s*;[^\n]*\n?`)
	wpAnchorRe = regexp.MustCompile(`(?m)^.*(That's all, stop editing|require_once\s*\(?\s*ABSPATH\s*\.\s*['"]wp-settings\.php).*$`)
)

var wpDatabase = map[string]string{
	"DB_NAME":     "define('DB_NAME', getenv('DB_DATABASE'));\n",
	"DB_USER":     "define('DB_USER', getenv('DB_USERNAME'));\n",
	"DB_PASSWORD": "define('DB_PASSWORD', getenv('DB_PASSWORD'));\n",
	"DB_HOST":     "define('DB_HOST', getenv('DB_HOST') . ':' . getenv('DB_PORT'));\n",
}

const wpAddress = `// Added by Envoryx: the site answers on the address it is opened with, and the proxy
// in front of it terminates HTTPS.
if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') {
    $_SERVER['HTTPS'] = 'on';
}
if (!empty($_SERVER['HTTP_HOST'])) {
    define('WP_HOME', (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off' ? 'https' : 'http') . '://' . $_SERVER['HTTP_HOST']);
    define('WP_SITEURL', WP_HOME);
}

`

// adaptWordPress points the DB_* constants at the injected variables and lets the site
// follow the address it is opened with (the old WP_HOME/WP_SITEURL go).
func adaptWordPress(src string) (string, error) {
	seen := map[string]bool{}
	out := wpDefineRe.ReplaceAllStringFunc(src, func(m string) string {
		name := wpDefineRe.FindStringSubmatch(m)[1]
		if seen[name] {
			return ""
		}
		seen[name] = true
		return wpDatabase[name] // "" for WP_HOME and WP_SITEURL
	})
	var missing strings.Builder
	for _, k := range []string{"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_HOST"} {
		if !seen[k] {
			missing.WriteString(wpDatabase[k])
		}
	}
	insert := missing.String() + wpAddress
	if loc := wpAnchorRe.FindStringIndex(out); loc != nil {
		return out[:loc[0]] + insert + out[loc[0]:], nil
	}
	// No anchor: right after the opening tag.
	if i := strings.Index(out, "<?php"); i >= 0 {
		j := i + len("<?php")
		return out[:j] + "\n" + insert + out[j:], nil
	}
	return "", fmt.Errorf("wp-config.php is not PHP code")
}

// adaptJoomla sets the connection and the paths of JConfig. Its properties only take
// constant expressions, so the values are written out.
func adaptJoomla(src string, db Database) string {
	dbtype := "mysqli"
	if db.Variant == "postgresql" {
		dbtype = "pgsql"
	}
	host := db.Host
	if db.Port != 0 {
		host += ":" + strconv.Itoa(db.Port)
	}
	values := []struct{ prop, expr string }{
		{"dbtype", phpQuote(dbtype)},
		{"host", phpQuote(host)},
		{"user", phpQuote(db.User)},
		{"password", phpQuote(db.Password)},
		{"db", phpQuote(db.Name)},
		{"live_site", "''"},
	}
	for _, v := range values {
		src = setProperty(src, v.prop, v.expr)
	}
	// Absolute paths of the old server.
	src = setPathProperty(src, "log_path", []string{"administrator/logs", "logs"})
	src = setPathProperty(src, "tmp_path", []string{"tmp"})
	return src
}

func propertyRe(prop string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^([ \t]*(?:public|var)\s+\$` + prop + `\s*=\s*)((?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|[^;\n]*))(\s*;)`)
}

func setProperty(src, prop, expr string) string {
	re := propertyRe(prop)
	return re.ReplaceAllStringFunc(src, func(m string) string {
		g := re.FindStringSubmatch(m)
		return g[1] + expr + g[3]
	})
}

// setPathProperty replaces an absolute path by the same directory below the site.
func setPathProperty(src, prop string, suffixes []string) string {
	re := propertyRe(prop)
	return re.ReplaceAllStringFunc(src, func(m string) string {
		g := re.FindStringSubmatch(m)
		old := strings.Trim(g[2], `'"`)
		for _, s := range suffixes {
			if old == s || strings.HasSuffix(strings.TrimRight(old, "/"), "/"+s) {
				return g[1] + "__DIR__ . " + phpQuote("/"+s) + g[3]
			}
		}
		return m
	})
}

func phpQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
