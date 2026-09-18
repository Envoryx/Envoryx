package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a uniqueness constraint would be violated.
var ErrConflict = errors.New("conflict")

// Store bundles all repositories on top of one database handle.
type Store struct {
	db       *sql.DB
	Users    *Users
	Sessions *Sessions
	Projects *Projects
	Settings *Settings
	Audit    *Audit
	Backups  *Backups
}

// New creates the repositories.
func New(db *sql.DB) *Store {
	return &Store{
		db:       db,
		Users:    &Users{db: db},
		Sessions: &Sessions{db: db},
		Projects: &Projects{db: db},
		Settings: &Settings{db: db},
		Audit:    &Audit{db: db},
		Backups:  &Backups{db: db},
	}
}

// DB exposes the underlying handle for health checks.
func (s *Store) DB() *sql.DB { return s.db }

// NewID returns a random UUID v4.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func now() time.Time { return time.Now().UTC() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isUniqueViolation detects SQLite UNIQUE constraint failures without importing driver internals.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
