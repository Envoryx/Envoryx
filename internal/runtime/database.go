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
var systemDatabases = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true, "postgres": true, "template0": true, "template1": true}

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

// Dialect describes how Staqio talks to a database flavour: container environment, data
// directory, health check and the administrative statements used by the UI. Statements
// only ever receive validated identifiers and generated passwords.
type Dialect struct {
	Variant      string
	Port         int
	DataDir      string
	Driver       string // Laravel DB_CONNECTION
	HasRoot      bool   // separate superuser password (MySQL/MariaDB) vs. owner = superuser (PostgreSQL)
	ContainerEnv func(cfg DatabaseConfig) []string
	Cmd          []string
	Health       []string
	// Client builds the argv + env to run a statement as the administrator.
	Client         func(cfg DatabaseConfig, sql string) (argv []string, env []string)
	ListDatabases  string
	CreateDatabase func(name, user string) string
	DropDatabase   func(name string) string
	AlterPassword  func(user, password string) string
	// MajorUpgradeInPlace reports whether the server upgrades an existing data directory
	// across major versions on its own.
	MajorUpgradeInPlace bool
}

var dialects = map[string]Dialect{
	"mariadb": {
		Variant: "mariadb", Port: 3306, DataDir: "/var/lib/mysql", Driver: "mysql", HasRoot: true,
		ContainerEnv: func(c DatabaseConfig) []string {
			return []string{"MARIADB_ROOT_PASSWORD=" + c.RootPassword, "MARIADB_DATABASE=" + c.Database, "MARIADB_USER=" + c.Username, "MARIADB_PASSWORD=" + c.Password, "MARIADB_AUTO_UPGRADE=1"}
		},
		Cmd:    []string{"--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci"},
		Health: []string{"healthcheck.sh", "--connect", "--innodb_initialized"},
		Client: func(c DatabaseConfig, sql string) ([]string, []string) {
			return []string{"mariadb", "-uroot", "-N", "-B", "-e", sql}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
		ListDatabases: "SHOW DATABASES",
		CreateDatabase: func(n, u string) string {
			return fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'; FLUSH PRIVILEGES;", n, n, u)
		},
		DropDatabase: func(n string) string { return fmt.Sprintf("DROP DATABASE `%s`", n) },
		AlterPassword: func(u, p string) string {
			return fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s'; FLUSH PRIVILEGES;", u, p)
		},
		MajorUpgradeInPlace: true,
	},
	"mysql": {
		Variant: "mysql", Port: 3306, DataDir: "/var/lib/mysql", Driver: "mysql", HasRoot: true,
		ContainerEnv: func(c DatabaseConfig) []string {
			return []string{"MYSQL_ROOT_PASSWORD=" + c.RootPassword, "MYSQL_DATABASE=" + c.Database, "MYSQL_USER=" + c.Username, "MYSQL_PASSWORD=" + c.Password}
		},
		Cmd:    []string{"--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci"},
		Health: []string{"mysqladmin", "ping", "-h", "127.0.0.1"},
		Client: func(c DatabaseConfig, sql string) ([]string, []string) {
			return []string{"mysql", "-uroot", "-N", "-B", "-e", sql}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
		ListDatabases: "SHOW DATABASES",
		CreateDatabase: func(n, u string) string {
			return fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'; FLUSH PRIVILEGES;", n, n, u)
		},
		DropDatabase: func(n string) string { return fmt.Sprintf("DROP DATABASE `%s`", n) },
		AlterPassword: func(u, p string) string {
			return fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s'; FLUSH PRIVILEGES;", u, p)
		},
		MajorUpgradeInPlace: true,
	},
	"postgresql": {
		Variant: "postgresql", Port: 5432, DataDir: "/var/lib/postgresql/data", Driver: "pgsql", HasRoot: false,
		ContainerEnv: func(c DatabaseConfig) []string {
			return []string{"POSTGRES_USER=" + c.Username, "POSTGRES_PASSWORD=" + c.Password, "POSTGRES_DB=" + c.Database}
		},
		Health: []string{"pg_isready", "-h", "127.0.0.1"},
		Client: func(c DatabaseConfig, sql string) ([]string, []string) {
			return []string{"psql", "-U", c.Username, "-d", "postgres", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-c", sql}, []string{"PGPASSWORD=" + c.Password}
		},
		ListDatabases:       "SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname",
		CreateDatabase:      func(n, u string) string { return fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s" ENCODING 'UTF8'`, n, u) },
		DropDatabase:        func(n string) string { return fmt.Sprintf(`DROP DATABASE "%s"`, n) },
		AlterPassword:       func(u, p string) string { return fmt.Sprintf(`ALTER USER "%s" WITH PASSWORD '%s'`, u, p) },
		MajorUpgradeInPlace: false,
	},
}

// DialectFor returns the dialect for a database variant.
func DialectFor(variant string) (Dialect, bool) {
	d, ok := dialects[variant]
	return d, ok
}

// DatabaseEnv returns the environment variables injected into application containers
// (Laravel naming plus a DSN for Symfony/Doctrine). Keys defined by the user win.
func DatabaseEnv(cfg DatabaseConfig, variant string) map[string]string {
	driver, port := "mysql", 3306
	if d, ok := dialects[variant]; ok {
		driver, port = d.Driver, d.Port
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

// ServiceConfig is the configuration of auxiliary services (Redis, Mailpit).
type ServiceConfig struct {
	// HostPort publishes the service's primary port (Redis 6379, Mailpit web UI 8025) on the host.
	HostPort int `json:"hostPort"`
}

// RedisEnv returns the variables injected for a Redis service.
func RedisEnv() map[string]string {
	return map[string]string{"REDIS_HOST": "redis", "REDIS_PORT": "6379", "REDIS_URL": "redis://redis:6379"}
}

// MailpitEnv returns the variables injected for a Mailpit service (Laravel + Symfony naming).
func MailpitEnv() map[string]string {
	return map[string]string{"MAIL_MAILER": "smtp", "MAIL_HOST": "mailpit", "MAIL_PORT": "1025", "MAIL_ENCRYPTION": "null", "MAILER_DSN": "smtp://mailpit:1025"}
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
