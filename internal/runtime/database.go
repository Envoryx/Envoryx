package runtime

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
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

// systemDatabases are never listed, created or dropped through Envoryx.
var systemDatabases = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true, "postgres": true, "template0": true, "template1": true, "admin": true, "config": true, "local": true}

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

// Dialect describes how Envoryx talks to a database flavour: container environment, data
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
	// Dump writes a logical backup of the primary database to stdout.
	Dump func(cfg DatabaseConfig) (argv []string, env []string)
	// Restore reads a dump from stdin into the primary database.
	Restore func(cfg DatabaseConfig) (argv []string, env []string)
	// URL builds the connection string injected as DATABASE_URL (nil = driver://user:pw@host:port/db).
	URL func(cfg DatabaseConfig) string
	// ExtraEnv adds flavour-specific variables (e.g. MONGODB_URI).
	ExtraEnv func(cfg DatabaseConfig) map[string]string
	// DumpFormat describes the backup payload ("sql" or "archive").
	DumpFormat string
}

// mongoURI builds a connection string authenticating against the admin database.
func mongoURI(c DatabaseConfig, host string, db string) string {
	u := fmt.Sprintf("mongodb://%s:%s@%s:27017/%s?authSource=admin", c.Username, c.Password, host, db)
	return u
}

// mongoClient is the argv prefix of an administrative mongosh call. The connection string
// travels in the environment (read by the script), not in argv.
const mongoClientPrelude = "const conn = Mongo(process.env.ENVORYX_MONGO_URI); const admin = conn.getDB('admin'); "

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
		Dump: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mariadb-dump", "-uroot", "--single-transaction", "--quick", "--routines", "--triggers", "--events", "--default-character-set=utf8mb4", "--", c.Database}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
		Restore: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mariadb", "-uroot", "--", c.Database}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
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
		Dump: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mysqldump", "-uroot", "--single-transaction", "--quick", "--routines", "--triggers", "--events", "--default-character-set=utf8mb4", "--", c.Database}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
		Restore: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mysql", "-uroot", "--", c.Database}, []string{"MYSQL_PWD=" + c.RootPassword}
		},
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
		Dump: func(c DatabaseConfig) ([]string, []string) {
			return []string{"pg_dump", "-U", c.Username, "--clean", "--if-exists", "--no-owner", "--no-privileges", "--", c.Database}, []string{"PGPASSWORD=" + c.Password}
		},
		Restore: func(c DatabaseConfig) ([]string, []string) {
			return []string{"psql", "-U", c.Username, "-v", "ON_ERROR_STOP=1", "-q", "-d", c.Database}, []string{"PGPASSWORD=" + c.Password}
		},
	},
}

func init() {
	dialects["mongodb"] = Dialect{
		Variant: "mongodb", Port: 27017, DataDir: "/data/db", Driver: "mongodb", HasRoot: false, DumpFormat: "archive",
		ContainerEnv: func(c DatabaseConfig) []string {
			return []string{"MONGO_INITDB_ROOT_USERNAME=" + c.Username, "MONGO_INITDB_ROOT_PASSWORD=" + c.Password, "MONGO_INITDB_DATABASE=" + c.Database}
		},
		Health: []string{"mongosh", "--quiet", "--norc", "--eval", "db.adminCommand('ping').ok ? quit(0) : quit(1)"},
		Client: func(c DatabaseConfig, js string) ([]string, []string) {
			return []string{"mongosh", "--quiet", "--norc", "--nodb", "--eval", mongoClientPrelude + js}, []string{"ENVORYX_MONGO_URI=" + mongoURI(c, "127.0.0.1", "admin")}
		},
		ListDatabases: "admin.adminCommand({listDatabases: 1}).databases.forEach(d => print(d.name))",
		// MongoDB creates databases lazily; a first collection makes it visible.
		CreateDatabase: func(n, _ string) string { return fmt.Sprintf("conn.getDB('%s').createCollection('envoryx_init')", n) },
		DropDatabase:   func(n string) string { return fmt.Sprintf("conn.getDB('%s').dropDatabase()", n) },
		AlterPassword:  func(u, p string) string { return fmt.Sprintf("admin.changeUserPassword('%s', '%s')", u, p) },
		// Major versions must be upgraded one step at a time (feature compatibility version).
		MajorUpgradeInPlace: false,
		// The database tools only accept credentials via --uri/--password; the URI is
		// therefore visible in the container's process list for the duration of the dump.
		Dump: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mongodump", "--quiet", "--archive", "--db", c.Database, "--uri", mongoURI(c, "127.0.0.1", "admin")}, nil
		},
		Restore: func(c DatabaseConfig) ([]string, []string) {
			return []string{"mongorestore", "--quiet", "--archive", "--drop", "--nsInclude", c.Database + ".*", "--uri", mongoURI(c, "127.0.0.1", "admin")}, nil
		},
		URL: func(c DatabaseConfig) string { return mongoURI(c, "database", c.Database) },
		ExtraEnv: func(c DatabaseConfig) map[string]string {
			return map[string]string{"MONGODB_URI": mongoURI(c, "database", c.Database), "MONGODB_DATABASE": c.Database}
		},
	}
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
	d, ok := dialects[variant]
	if ok {
		driver, port = d.Driver, d.Port
	}
	env := map[string]string{
		"DB_CONNECTION": driver,
		"DB_HOST":       "database",
		"DB_PORT":       strconv.Itoa(port),
		"DB_DATABASE":   cfg.Database,
		"DB_USERNAME":   cfg.Username,
		"DB_PASSWORD":   cfg.Password,
		"DATABASE_URL":  fmt.Sprintf("%s://%s:%s@database:%d/%s", driver, cfg.Username, cfg.Password, port, cfg.Database),
	}
	if ok && d.URL != nil {
		env["DATABASE_URL"] = d.URL(cfg)
	}
	if ok && d.ExtraEnv != nil {
		for k, v := range d.ExtraEnv(cfg) {
			env[k] = v
		}
	}
	return env
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

// MailpitEnv returns the variables injected for a Mailpit service (Laravel, Symfony and
// the SMTP_* pair Node mailers such as nodemailer examples read).
func MailpitEnv() map[string]string {
	return map[string]string{"MAIL_MAILER": "smtp", "MAIL_HOST": "mailpit", "MAIL_PORT": "1025", "MAIL_ENCRYPTION": "null", "MAILER_DSN": "smtp://mailpit:1025", "SMTP_HOST": "mailpit", "SMTP_PORT": "1025"}
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
