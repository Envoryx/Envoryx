package runtime

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/seramos/staqio/internal/validate"
)

// DatabaseConfig is the per-project database configuration stored in
// project_services.config. Passwords never leave the backend except through the explicit
// credentials endpoint.
type DatabaseConfig struct {
	RootPassword string `json:"rootPassword"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	// HostPort publishes the database on the Docker host for external clients (0 = off).
	HostPort int `json:"hostPort"`
}

// Redacted returns the configuration without secrets for API responses.
func (c DatabaseConfig) Redacted() map[string]any {
	return map[string]any{"database": c.Database, "username": c.Username, "hostPort": c.HostPort}
}

const (
	// PasswordLength is the length of generated database passwords.
	PasswordLength = 24
	// passwordAlphabet avoids characters that need quoting in shells, URLs or ini files.
	passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// GeneratePassword returns a cryptographically random password from a shell/URL-safe alphabet.
func GeneratePassword(length int) (string, error) {
	if length < 12 {
		length = 12
	}
	out := make([]byte, length)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// NewDatabaseConfig derives identifiers from the project slug and generates fresh passwords.
func NewDatabaseConfig(slug string) (DatabaseConfig, error) {
	root, err := GeneratePassword(PasswordLength)
	if err != nil {
		return DatabaseConfig{}, err
	}
	pw, err := GeneratePassword(PasswordLength)
	if err != nil {
		return DatabaseConfig{}, err
	}
	ident := DBIdentifier(slug)
	return DatabaseConfig{RootPassword: root, Database: ident, Username: ident, Password: pw}, nil
}

// DBIdentifier turns a project slug into a safe database/user name (letters, digits, "_").
func DBIdentifier(slug string) string {
	s := strings.ReplaceAll(slug, "-", "_")
	if len(s) > 32 {
		s = s[:32]
	}
	s = strings.Trim(s, "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "app_" + s
	}
	return s
}

var dbNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// systemDatabases are never listed, created or dropped through Staqio.
var systemDatabases = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true}

// ValidateDatabaseName checks a user supplied database name.
func ValidateDatabaseName(name string) error {
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("%w: database name must match [a-z][a-z0-9_]* (max 63 chars)", validate.ErrInvalid)
	}
	if systemDatabases[name] {
		return fmt.Errorf("%w: %q is a system database", validate.ErrInvalid, name)
	}
	return nil
}

// IsSystemDatabase reports whether a name belongs to the server itself.
func IsSystemDatabase(name string) bool { return systemDatabases[name] }

// DatabaseEnv returns the environment variables injected into application containers
// (Laravel naming plus a DSN for Symfony/Doctrine). Keys defined by the user win.
func DatabaseEnv(cfg DatabaseConfig, variant string) map[string]string {
	driver := "mysql"
	port := 3306
	if variant == "postgresql" {
		driver, port = "pgsql", 5432
	}
	return map[string]string{
		"DB_CONNECTION": driver,
		"DB_HOST":       "database",
		"DB_PORT":       strconv.Itoa(port),
		"DB_DATABASE":   cfg.Database,
		"DB_USERNAME":   cfg.Username,
		"DB_PASSWORD":   cfg.Password,
		"DATABASE_URL":  fmt.Sprintf("%s://%s:%s@database:%d/%s", driver, cfg.Username, cfg.Password, port, cfg.Database),
	}
}

// CompareVersions returns -1, 0 or 1 comparing dotted numeric versions.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
