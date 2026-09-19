// Package db opens the Envoryx SQLite database and applies embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Options tune Open.
type Options struct {
	// BeforeMigrate runs once, before the first pending migration, when the database is
	// behind the binary. from is the applied schema version, to the target version. An
	// error aborts the start without touching the schema.
	BeforeMigrate func(ctx context.Context, sqlDB *sql.DB, from, to int) error
}

// Open opens (and creates if necessary) the SQLite database at path and applies all
// pending migrations. Use ":memory:" for an in-memory database (tests).
func Open(ctx context.Context, path string, log *slog.Logger) (*sql.DB, error) {
	return OpenWith(ctx, path, log, Options{})
}

// ErrCorrupt is returned when the database file fails SQLite's integrity check.
var ErrCorrupt = errors.New("database integrity check failed")

// OpenWith is Open with options. A file database is integrity-checked before anything
// touches it; a damaged file is refused (ErrCorrupt) rather than migrated or served.
func OpenWith(ctx context.Context, path string, log *slog.Logger, opts Options) (*sql.DB, error) {
	sqlDB, err := OpenRaw(ctx, path, log)
	if err != nil {
		return nil, err
	}
	if path != ":memory:" {
		if err := Check(ctx, sqlDB); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}
	if err := migrate(ctx, sqlDB, log, opts.BeforeMigrate); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

// OpenRaw opens the database without applying migrations. It is used before a restore
// and for snapshots taken ahead of migrations; everything else goes through Open.
func OpenRaw(ctx context.Context, path string, log *slog.Logger) (*sql.DB, error) {
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	} else {
		dsn = "file::memory:?_pragma=foreign_keys(ON)"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles a single writer; keeping one connection avoids "database is locked"
	// surprises and makes the in-memory variant behave like a single database.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("could not restrict database file permissions", "path", path, "err", err)
		}
	}
	return sqlDB, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		idx := strings.Index(name, "_")
		if idx <= 0 {
			return nil, fmt.Errorf("migration %q: name must be <version>_<name>.sql", name)
		}
		v, err := strconv.Atoi(name[:idx])
		if err != nil {
			return nil, fmt.Errorf("migration %q: invalid version: %w", name, err)
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// Check runs SQLite's full integrity check. It reads the whole file, which for the
// kilobytes-to-megabytes Envoryx keeps takes milliseconds.
func Check(ctx context.Context, sqlDB *sql.DB) error {
	rows, err := sqlDB.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrCorrupt, strings.Join(problems, "; "))
	}
	return nil
}

// LatestVersion returns the schema version this binary migrates to.
func LatestVersion() int {
	migrations, err := loadMigrations()
	if err != nil || len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

// Migrate applies all migrations that have not been applied yet. Each migration runs in
// its own transaction. A database that is newer than the binary is refused.
func Migrate(ctx context.Context, sqlDB *sql.DB, log *slog.Logger) error {
	return migrate(ctx, sqlDB, log, nil)
}

func migrate(ctx context.Context, sqlDB *sql.DB, log *slog.Logger, before func(context.Context, *sql.DB, int, int) error) error {
	if _, err := sqlDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	applied := map[int]bool{}
	maxApplied := 0
	rows, err := sqlDB.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return err
		}
		applied[v] = true
		if v > maxApplied {
			maxApplied = v
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	if maxApplied > latest {
		return fmt.Errorf("database schema version %d is newer than this Envoryx build supports (%d); refusing to start", maxApplied, latest)
	}

	if before != nil && maxApplied < latest && len(applied) > 0 {
		// Only an existing database is worth a snapshot; a fresh one has nothing to lose.
		if err := before(ctx, sqlDB, maxApplied, latest); err != nil {
			return fmt.Errorf("before migration: %w", err)
		}
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		log.Info("applying migration", "version", m.version, "name", m.name)
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func SchemaVersion(ctx context.Context, sqlDB *sql.DB) (int, error) {
	var v sql.NullInt64
	if err := sqlDB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}
