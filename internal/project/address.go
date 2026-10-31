package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
)

// After a rename the project answers at a new address. What reads it from ENVORYX_URL
// (APP_URL=${ENVORYX_URL} of the Laravel and Shopware templates, Symfony's DEFAULT_URI,
// Craft's PRIMARY_SITE_URL) follows by itself with the recreated containers. Two places
// keep the address as text and are moved here: a Craft .env whose PRIMARY_SITE_URL holds
// the old address itself, and Shopware's sales channel domains, which system:install
// writes into the database. Both are best effort: the rename has succeeded by then, so a
// failure is logged rather than undoing it.

// followAddress moves the old project address to the new one where an application keeps
// it as text. p is the renamed project; its containers exist and run when it runs.
func (m *Manager) followAddress(ctx context.Context, p store.Project, oldURL, newURL string) {
	if oldURL == "" || newURL == "" || oldURL == newURL || p.Service(store.ServicePHP) == nil {
		return
	}
	planner, err := m.planner()
	if err != nil {
		return
	}
	dir := planner.ProjectDir(p)
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	if exists("craft") && exists("vendor/craftcms/cms") {
		if changed, err := followCraftEnv(dir, oldURL); err != nil {
			m.log.Warn("point Craft's PRIMARY_SITE_URL at the new address", "project", p.Slug, "err", err)
		} else if changed {
			step(ctx, "Pointing PRIMARY_SITE_URL in .env at the new address")
		}
	}
	if exists("bin/console") && exists("vendor/shopware/core") {
		if err := m.followShopwareDomains(ctx, p, dir, oldURL, newURL); err != nil {
			m.log.Warn("move Shopware's sales channel domains to the new address", "project", p.Slug, "err", err)
		}
	}
}

// craftSiteURLLine matches a PRIMARY_SITE_URL assignment in a .env, quoted or not.
var craftSiteURLLine = regexp.MustCompile(`(?m)^PRIMARY_SITE_URL=(["']?)([^"'\r\n]*)(["']?)[ \t]*$`)

// followCraftEnv replaces a PRIMARY_SITE_URL that holds the old address (or a path below
// it) with the reference to ENVORYX_URL, which follows every later rename too. Any other
// value was set on purpose and stays. The .env in the project directory dir belongs to the
// project and may be a link out of it, so it's read and written through an os.Root of dir.
func followCraftEnv(dir, oldURL string) (bool, error) {
	b, err := readProjectFile(dir, ".env", maxProjectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old := strings.TrimRight(oldURL, "/")
	changed := false
	out := craftSiteURLLine.ReplaceAllFunc(b, func(line []byte) []byte {
		v := strings.TrimRight(string(craftSiteURLLine.FindSubmatch(line)[2]), "/")
		if v != old {
			return line
		}
		changed = true
		return []byte("PRIMARY_SITE_URL=${ENVORYX_URL}")
	})
	if !changed || bytes.Equal(out, b) {
		return false, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Stat(".env")
	if err != nil {
		return false, err
	}
	return true, root.WriteFile(".env", out, info.Mode().Perm())
}

// followShopwareDomains rewrites the sales channel domains on the old address to the new
// one, then clears Shopware's cache, which holds the domains it routes by. The database
// is started for it when the project is stopped.
func (m *Manager) followShopwareDomains(ctx context.Context, p store.Project, dir, oldURL, newURL string) error {
	svc, cfg, err := databaseOf(p, "")
	if err != nil {
		return nil // no database, no sales channels
	}
	if svc.Variant != "mariadb" && svc.Variant != "mysql" {
		return nil
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	moved := false
	err = m.withServiceRunning(ctx, p, svc.Kind, func(ctx context.Context) error {
		if err := m.waitForDatabase(ctx, p, svc, cfg, dialect); err != nil {
			return err
		}
		out, err := m.runSQL(ctx, p, svc, cfg, fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = %s AND table_name = 'sales_channel_domain'", sqlString(cfg.Database)))
		if err != nil || strings.TrimSpace(out) == "0" {
			return err // not installed yet: no sales channels
		}
		out, err = m.runSQL(ctx, p, svc, cfg, shopwareDomainSQL(cfg.Database, oldURL, newURL))
		if err != nil {
			return err
		}
		moved = strings.TrimSpace(out) != "0"
		return nil
	})
	if err != nil || !moved {
		return err
	}
	step(ctx, "Moving the Shopware sales channel domains to the new address")
	if c, err := m.ServiceContainer(ctx, p.ID, store.ServicePHP); err == nil && c.State == "running" {
		env, err := m.execEnv(store.ServicePHP)
		if err != nil {
			return err
		}
		var out bytes.Buffer
		code, err := m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{
			Cmd: []string{"php", "bin/console", "cache:clear", "--no-interaction"}, Env: append(env.Env, nonInteractiveEnv...),
			User: env.User, WorkingDir: env.WorkingDir, Stdout: &out, Stderr: &out,
		})
		if err == nil && code != 0 {
			err = fmt.Errorf("cache:clear exited with %d: %s", code, strings.TrimSpace(lastLines(out.String(), 5)))
		}
		return err
	}
	// Stopped: Shopware builds the cache again on the first request after the start.
	return clearShopwareCache(dir)
}

// clearShopwareCache empties var/cache in the project directory dir. It belongs to the
// project and may be a link out of it, so it's emptied through an os.Root of dir; a
// cache that isn't there (or leads out) is left alone.
func clearShopwareCache(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	cache, err := root.OpenRoot(filepath.Join("var", "cache"))
	if err != nil {
		return nil
	}
	defer cache.Close()
	entries, err := fs.ReadDir(cache.FS(), ".")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if err := cache.RemoveAll(e.Name()); err != nil {
			return fmt.Errorf("clear var/cache: %w", err)
		}
	}
	return nil
}

// shopwareDomainSQL moves the sales channel domains on oldURL (the address itself or a
// path below it, a language prefix such as /en) to newURL and prints how many it moved.
func shopwareDomainSQL(database, oldURL, newURL string) string {
	old := strings.TrimRight(oldURL, "/")
	like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(old) + "/%"
	return fmt.Sprintf("UPDATE `%s`.sales_channel_domain SET url = CONCAT(%s, SUBSTRING(url, %d)) WHERE url = %s OR url LIKE %s; SELECT ROW_COUNT();",
		strings.ReplaceAll(database, "`", "``"), sqlString(strings.TrimRight(newURL, "/")), len(old)+1, sqlString(old), sqlString(like))
}

// sqlString quotes s as a MySQL string literal.
func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}
