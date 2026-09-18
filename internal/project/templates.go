package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Template scaffolds a fresh application into an empty project directory. Steps are
// argv commands run in a transient container from the project's PHP image as the project
// owner, exactly like git operations; nothing is interpolated from user input.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Docroot the template expects (applied when the request leaves it empty).
	Docroot string `json:"docroot"`
	// RequiresDatabase refuses creation without a database service.
	RequiresDatabase bool `json:"requiresDatabase"`
	// RecommendedDatabase is preselected in the wizard.
	RecommendedDatabase string `json:"recommendedDatabase,omitempty"`
	// PHPExtensions are enabled in addition to the defaults.
	PHPExtensions []string `json:"phpExtensions,omitempty"`
	// Notes are shown after creation (next steps).
	Notes string `json:"notes,omitempty"`

	steps []templateStep
}

type templateStep struct {
	label string
	cmd   []string
	// files are written by Envoryx after the command (relative path → content generator).
	files map[string]func() (string, error)
}

const composerNoInteraction = "--no-interaction"

// wordpressInstall downloads and unpacks the latest WordPress with PHP only (phar +
// openssl are built into every PHP image, so no zip/curl binaries are needed).
const wordpressInstall = `
$tmp = sys_get_temp_dir() . '/envoryx-wp-' . getmypid();
mkdir($tmp, 0700, true);
$tgz = "$tmp/wordpress.tar.gz";
if (!copy('https://wordpress.org/latest.tar.gz', $tgz)) { fwrite(STDERR, "download failed\n"); exit(1); }
$p = new PharData($tgz);
$p->decompress();
$t = new PharData("$tmp/wordpress.tar");
$t->extractTo($tmp, null, true);
$src = "$tmp/wordpress";
$it = new RecursiveIteratorIterator(new RecursiveDirectoryIterator($src, FilesystemIterator::SKIP_DOTS), RecursiveIteratorIterator::SELF_FIRST);
foreach ($it as $item) {
  $dest = getcwd() . '/' . substr($item->getPathname(), strlen($src) + 1);
  if ($item->isDir()) { if (!is_dir($dest)) mkdir($dest, 0755, true); }
  else { copy($item->getPathname(), $dest); }
}
echo "WordPress unpacked\n";
`

var templates = []Template{
	{
		ID: "laravel", Name: "Laravel", Description: "composer create-project laravel/laravel – ready to run with the project database.",
		Docroot: "public", RecommendedDatabase: "mariadb",
		Notes: "Run “artisan migrate” from the Actions tab. Envoryx injects DB_* and REDIS_*/MAIL_* variables; they override .env.",
		steps: []templateStep{{label: "composer create-project", cmd: []string{"composer", "create-project", "laravel/laravel", ".", composerNoInteraction, "--prefer-dist"}}},
	},
	{
		ID: "symfony", Name: "Symfony", Description: "symfony/skeleton plus the webapp pack (Twig, Doctrine, forms, security…).",
		Docroot: "public", RecommendedDatabase: "postgresql",
		Notes: "DATABASE_URL is injected by Envoryx. Create the schema with “doctrine:migrations:migrate” (Symfony console action).",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "symfony/skeleton", ".", composerNoInteraction, "--prefer-dist"}},
			{label: "composer require webapp", cmd: []string{"composer", "require", "webapp", composerNoInteraction}},
		},
	},
	{
		ID: "wordpress", Name: "WordPress", Description: "Latest WordPress with a generated wp-config.php wired to the project database.",
		Docroot: "", RequiresDatabase: true, RecommendedDatabase: "mariadb", PHPExtensions: []string{"mysqli"},
		Notes: "Open the site to run the WordPress installer (site title, admin account).",
		steps: []templateStep{{label: "download WordPress", cmd: []string{"php", "-r", wordpressInstall}, files: map[string]func() (string, error){"wp-config.php": wordpressConfig}}},
	},
}

// Templates lists the available project templates.
func Templates() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TemplateByID finds a template.
func TemplateByID(id string) (Template, bool) {
	for _, t := range templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// wordpressConfig renders wp-config.php reading the injected DB_* variables at runtime.
func wordpressConfig() (string, error) {
	salts := make([]string, 8)
	for i := range salts {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		salts[i] = hex.EncodeToString(b)
	}
	names := []string{"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY", "AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT"}
	var b strings.Builder
	b.WriteString("<?php\n// Generated by Envoryx. Database settings come from the environment Envoryx injects.\n")
	b.WriteString("define('DB_NAME', getenv('DB_DATABASE') ?: 'wordpress');\n")
	b.WriteString("define('DB_USER', getenv('DB_USERNAME') ?: 'wordpress');\n")
	b.WriteString("define('DB_PASSWORD', getenv('DB_PASSWORD') ?: '');\n")
	b.WriteString("define('DB_HOST', (getenv('DB_HOST') ?: 'database') . ':' . (getenv('DB_PORT') ?: '3306'));\n")
	b.WriteString("define('DB_CHARSET', 'utf8mb4');\ndefine('DB_COLLATE', '');\n\n")
	for i, n := range names {
		fmt.Fprintf(&b, "define('%s', '%s');\n", n, salts[i])
	}
	b.WriteString("\n$table_prefix = 'wp_';\ndefine('WP_DEBUG', true);\ndefine('WP_DEBUG_DISPLAY', true);\ndefine('FS_METHOD', 'direct');\n")
	b.WriteString("// Behind Envoryx's proxy the TLS connection is terminated upstream.\n")
	b.WriteString("if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { $_SERVER['HTTPS'] = 'on'; }\n")
	b.WriteString("if (!defined('ABSPATH')) { define('ABSPATH', __DIR__ . '/'); }\nrequire_once ABSPATH . 'wp-settings.php';\n")
	return b.String(), nil
}

// applyTemplate scaffolds the template into the (empty) project directory.
func (m *Manager) applyTemplate(ctx context.Context, proj store.Project, tpl Template) error {
	php := proj.Service(store.ServicePHP)
	if php == nil {
		return fmt.Errorf("%w: template %s needs a PHP service", validate.ErrInvalid, tpl.ID)
	}
	if tpl.RequiresDatabase && proj.Service(store.ServiceDatabase) == nil {
		return fmt.Errorf("%w: template %s needs a database service", validate.ErrInvalid, tpl.ID)
	}
	paths, err := m.paths()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	planner := NewPlanner(paths, m.catalog)
	dir := planner.ProjectDir(proj)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: the project directory is not empty; templates need an empty directory", ErrConflict)
	}
	if err := m.engine.EnsureImage(ctx, php.Image, m.pullProgress(proj.Slug)); err != nil {
		return err
	}
	for i, step := range tpl.steps {
		spec := docker.ContainerSpec{
			Name:       fmt.Sprintf("envoryx-%s-template-%d-%d", proj.Slug, i, time.Now().UnixNano()%1_000_000),
			Image:      php.Image,
			Labels:     docker.ManagedLabels(proj.ID, proj.Slug, "template", paths.EnvoryxVersion),
			Env:        []string{"HOME=/tmp", "COMPOSER_HOME=/tmp/composer", "COMPOSER_NO_INTERACTION=1", "COMPOSER_MEMORY_LIMIT=-1"},
			Cmd:        step.cmd,
			WorkingDir: appMountTarget,
			User:       fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
			Mounts:     []docker.MountSpec{{Type: "bind", Source: planner.projectHostDir(proj), Target: appMountTarget}},
			// Composer downloads need DNS/internet: default bridge network.
			RestartPolicy: "no",
		}
		res, err := m.engine.RunOneShot(ctx, spec)
		if err != nil {
			return fmt.Errorf("template %s (%s): %w", tpl.ID, step.label, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("%w: template %s failed at %q: %s", ErrConflict, tpl.ID, step.label, lastLine(res.Stdout+"\n"+res.Stderr))
		}
		for rel, gen := range step.files {
			content, err := gen()
			if err != nil {
				return err
			}
			path, err := validate.ResolveUnder(dir, rel)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", rel, err)
			}
			_ = os.Chown(path, paths.PUID, paths.PGID)
		}
	}
	chownTree(dir, paths.PUID, paths.PGID)
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", proj.ID, map[string]any{"name": proj.Name, "template": tpl.ID})
	return nil
}
