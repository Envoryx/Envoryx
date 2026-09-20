// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/store"
)

// The rescue CLI works directly on the SQLite database and is meant for the shell of the
// running container (docker exec envoryx envoryx admin …). SQLite serialises the writes
// with the server's; every request re-reads sessions and accounts, so a reset takes
// effect immediately without a restart.
const adminUsage = `Usage: envoryx admin <command> [flags]

Recover access to the web interface when the credentials are lost. Run inside the
container: docker exec -it envoryx envoryx admin <command>

Commands:
  users                       list the accounts
  reset-password [flags]      set a new password for an account and end its sessions
      --user NAME             account to change (default: the only account)
      --password PW           the new password (default: a generated one, printed once)
  logout-all                  end every browser session (everyone has to sign in again)
  revoke-tokens               delete every API token (MCP, SSH/SFTP, scripts)
  reset --yes                 delete all accounts, sessions and tokens; the setup page
                              appears again on the next visit. Projects are untouched.

Every command is recorded in the audit log as "cli".
`

// adminCLI runs one rescue command against the store. It is separated from the
// database plumbing so tests can drive it with an in-memory database.
type adminCLI struct {
	store *store.Store
	audit *audit.Logger
	out   io.Writer
	now   func() time.Time
}

func adminCommand(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, adminUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envoryx admin: configuration:", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := os.Stat(cfg.DatabasePath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "envoryx admin: no database at %s yet – start the server once, then retry\n", cfg.DatabasePath)
		return 1
	}
	sqlDB, err := db.OpenRaw(ctx, cfg.DatabasePath, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "envoryx admin: database:", err)
		return 1
	}
	defer sqlDB.Close()
	// Never migrate from here: the server does that with an instance backup first. A
	// mismatch means this binary and the database belong to different Envoryx versions.
	schema, err := db.SchemaVersion(ctx, sqlDB)
	if err != nil {
		fmt.Fprintln(os.Stderr, "envoryx admin: database:", err)
		return 1
	}
	if schema != db.LatestVersion() {
		fmt.Fprintf(os.Stderr, "envoryx admin: database schema %d, this build expects %d – start the server once so it migrates the database, then retry\n", schema, db.LatestVersion())
		return 1
	}
	st := store.New(sqlDB)
	cli := &adminCLI{store: st, audit: audit.New(st.Audit, log), out: os.Stdout, now: func() time.Time { return time.Now().UTC() }}
	if err := cli.run(ctx, args); err != nil {
		if errors.Is(err, errAdminUsage) {
			fmt.Fprint(os.Stderr, adminUsage)
			return 2
		}
		fmt.Fprintln(os.Stderr, "envoryx admin:", err)
		return 1
	}
	return 0
}

var errAdminUsage = errors.New("usage")

func (c *adminCLI) run(ctx context.Context, args []string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "users":
		return c.users(ctx)
	case "reset-password":
		fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		user := fs.String("user", "", "account name")
		password := fs.String("password", "", "new password")
		if err := fs.Parse(rest); err != nil || fs.NArg() > 0 {
			return errAdminUsage
		}
		return c.resetPassword(ctx, *user, *password)
	case "logout-all":
		return c.logoutAll(ctx)
	case "revoke-tokens":
		return c.revokeTokens(ctx)
	case "reset":
		fs := flag.NewFlagSet("reset", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		yes := fs.Bool("yes", false, "confirm")
		if err := fs.Parse(rest); err != nil || fs.NArg() > 0 {
			return errAdminUsage
		}
		if !*yes {
			return errors.New("reset deletes every account, session and API token; confirm with --yes")
		}
		return c.reset(ctx)
	default:
		return fmt.Errorf("unknown command %q: %w", cmd, errAdminUsage)
	}
}

func (c *adminCLI) users(ctx context.Context) error {
	users, err := c.store.Users.List(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Fprintln(c.out, "No accounts – the setup page is shown on the next visit.")
		return nil
	}
	tw := tabwriter.NewWriter(c.out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLE\tCREATED\tPASSWORD CHANGED (UTC)")
	for _, u := range users {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Username, u.Role, u.CreatedAt.Format("2006-01-02"), u.UpdatedAt.Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

func (c *adminCLI) resetPassword(ctx context.Context, username, password string) error {
	user, err := c.pickUser(ctx, username)
	if err != nil {
		return err
	}
	generated := password == ""
	if generated {
		password, err = generatePassword(20)
		if err != nil {
			return err
		}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := c.store.Users.UpdatePassword(ctx, user.ID, hash); err != nil {
		return err
	}
	if err := c.store.Sessions.DeleteByUser(ctx, user.ID); err != nil {
		return err
	}
	c.audit.LogAs(ctx, "cli", audit.ActionPasswordReset, "user", user.ID, map[string]any{"username": user.Username, "generated": generated})
	if generated {
		fmt.Fprintf(c.out, "New password for %s (shown once, change it after signing in):\n\n  %s\n\n", user.Username, password)
	} else {
		fmt.Fprintf(c.out, "Password of %s changed.\n", user.Username)
	}
	fmt.Fprintln(c.out, "All sessions of this account were ended. Failed sign-in attempts are still rate-limited for up to 15 minutes.")
	return nil
}

// pickUser resolves --user, or the single account when there is exactly one.
func (c *adminCLI) pickUser(ctx context.Context, username string) (store.User, error) {
	if username != "" {
		u, err := c.store.Users.ByUsername(ctx, username)
		if errors.Is(err, store.ErrNotFound) {
			return store.User{}, fmt.Errorf("no account named %q (envoryx admin users lists them)", username)
		}
		return u, err
	}
	users, err := c.store.Users.List(ctx)
	if err != nil {
		return store.User{}, err
	}
	switch len(users) {
	case 0:
		return store.User{}, errors.New("no accounts exist – open the web interface, the setup page creates the first one")
	case 1:
		return users[0], nil
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Username
	}
	return store.User{}, fmt.Errorf("several accounts exist (%s); choose one with --user", strings.Join(names, ", "))
}

func (c *adminCLI) logoutAll(ctx context.Context) error {
	n, err := c.store.Sessions.DeleteAll(ctx)
	if err != nil {
		return err
	}
	c.audit.LogAs(ctx, "cli", audit.ActionSessionsRevoked, "session", "", map[string]any{"count": n})
	fmt.Fprintf(c.out, "%d session(s) ended.\n", n)
	return nil
}

func (c *adminCLI) revokeTokens(ctx context.Context) error {
	n, err := c.store.Tokens.DeleteAll(ctx)
	if err != nil {
		return err
	}
	c.audit.LogAs(ctx, "cli", audit.ActionTokensRevoked, "token", "", map[string]any{"count": n})
	fmt.Fprintf(c.out, "%d API token(s) revoked.\n", n)
	return nil
}

func (c *adminCLI) reset(ctx context.Context) error {
	users, err := c.store.Users.List(ctx)
	if err != nil {
		return err
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Username
	}
	n, err := c.store.Users.DeleteAll(ctx)
	if err != nil {
		return err
	}
	c.audit.LogAs(ctx, "cli", audit.ActionAccountsReset, "user", "", map[string]any{"count": n, "usernames": names})
	fmt.Fprintf(c.out, "%d account(s) deleted with their sessions and API tokens. Open the web interface to create the administrator account again.\n", n)
	return nil
}

// generatePassword returns n characters from an unambiguous alphabet (no 0/O, 1/l/I).
func generatePassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b), nil
}
