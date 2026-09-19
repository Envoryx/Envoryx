// Package config loads the Envoryx runtime configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration of the Envoryx server.
type Config struct {
	// ListenAddr is the address the HTTP server binds to, e.g. ":8787".
	ListenAddr string

	// ConfigDir is the persistent configuration directory inside the container (default /config).
	ConfigDir string
	// ProjectsDir is the directory that contains all project directories (default /projects).
	ProjectsDir string
	// BackupsDir is where project backups are stored. Defaults to /backups when that
	// directory exists (i.e. was mounted), otherwise to <ConfigDir>/backups.
	BackupsDir string

	// ConfigHostPath and ProjectsHostPath are the host-side paths behind ConfigDir and
	// ProjectsDir. They are required for bind mounts into project containers. Empty
	// means "auto-detect from the Envoryx container's own mounts".
	ConfigHostPath   string
	ProjectsHostPath string

	// DockerHost overrides the Docker endpoint (DOCKER_HOST). Empty = default socket.
	DockerHost string

	// DatabasePath is the SQLite database file.
	DatabasePath string

	// Session lifetimes.
	SessionIdleTimeout     time.Duration
	SessionAbsoluteTimeout time.Duration

	// UpdateCheck lets Envoryx ask GitHub once a day whether a newer release exists (one
	// anonymous GET; the UI then shows a hint). ENVORYX_UPDATE_CHECK=false switches it off.
	UpdateCheck bool

	// ShutdownGrace is how long running project operations (image pulls, backups) may
	// finish after SIGTERM before they are abandoned. Keep it below the container's stop
	// timeout (Docker default 10 s; Unraid: Settings → Docker → stop timeout).
	ShutdownGrace time.Duration

	// SecureCookies marks the session cookie as Secure. Enable when Envoryx is served over HTTPS.
	SecureCookies bool

	// Port range used for project web servers published on the host.
	PortRangeStart int
	PortRangeEnd   int

	// PUID/PGID are the uid/gid project containers run their workers as, so files created by
	// PHP/Node inside a project belong to the owner of the project directory.
	PUID int
	PGID int

	// AdminUser/AdminPassword optionally bootstrap the first admin user non-interactively.
	AdminUser     string
	AdminPassword string

	// LogLevel is debug|info|warn|error, LogFormat is json|text.
	LogLevel  string
	LogFormat string

	// ProxyHTTP / ProxyHTTPS are the listen addresses of the embedded reverse proxy inside
	// the container (":80" / ":443"). Empty disables the respective listener.
	ProxyHTTP  string
	ProxyHTTPS string
	// SSHListen is the embedded SSH server address (":2222"); empty disables it.
	SSHListen string

	// PublicHost is the host name or IP the browser should use for project links (ports are
	// published on the Docker host, which may differ from the address Envoryx is reached at,
	// e.g. when the Envoryx container has its own macvlan IP). Empty = browser address bar.
	PublicHost string

	// DevMode relaxes a few things for local development (e.g. text logs, CORS for the Vite dev server).
	DevMode bool
	// AllowNetworkFS lets Envoryx start with /config on NFS/SMB (not recommended).
	AllowNetworkFS bool
	// DevOrigin is the allowed browser origin in dev mode (Vite dev server).
	DevOrigin string
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	c := Config{
		ListenAddr:             env("ENVORYX_LISTEN", ":8787"),
		ConfigDir:              env("ENVORYX_CONFIG_DIR", "/config"),
		ProjectsDir:            env("ENVORYX_PROJECTS_DIR", "/projects"),
		ConfigHostPath:         env("ENVORYX_CONFIG_HOST_PATH", ""),
		ProjectsHostPath:       env("ENVORYX_PROJECTS_HOST_PATH", ""),
		DockerHost:             env("DOCKER_HOST", ""),
		SessionIdleTimeout:     envDuration("ENVORYX_SESSION_IDLE_TIMEOUT", 12*time.Hour),
		SessionAbsoluteTimeout: envDuration("ENVORYX_SESSION_ABSOLUTE_TIMEOUT", 7*24*time.Hour),
		ShutdownGrace:          envDuration("ENVORYX_SHUTDOWN_GRACE", 8*time.Second),
		UpdateCheck:            envBool("ENVORYX_UPDATE_CHECK", true),
		SecureCookies:          envBool("ENVORYX_SECURE_COOKIES", false),
		PortRangeStart:         envInt("ENVORYX_PORT_RANGE_START", 20000),
		PortRangeEnd:           envInt("ENVORYX_PORT_RANGE_END", 20999),
		PUID:                   envInt("PUID", 1000),
		PGID:                   envInt("PGID", 1000),
		PublicHost:             env("ENVORYX_PUBLIC_HOST", ""),
		ProxyHTTP:              envAllowEmpty("ENVORYX_PROXY_HTTP", ":80"),
		ProxyHTTPS:             envAllowEmpty("ENVORYX_PROXY_HTTPS", ":443"),
		SSHListen:              envAllowEmpty("ENVORYX_SSH", ":2222"),
		AdminUser:              env("ENVORYX_ADMIN_USER", ""),
		AdminPassword:          env("ENVORYX_ADMIN_PASSWORD", ""),
		LogLevel:               strings.ToLower(env("ENVORYX_LOG_LEVEL", "info")),
		LogFormat:              strings.ToLower(env("ENVORYX_LOG_FORMAT", "json")),
		DevMode:                envBool("ENVORYX_DEV", false),
		AllowNetworkFS:         envBool("ENVORYX_ALLOW_NETWORK_FS", false),
		DevOrigin:              env("ENVORYX_DEV_ORIGIN", "http://localhost:5173"),
	}
	c.DatabasePath = env("ENVORYX_DATABASE_PATH", filepath.Join(c.ConfigDir, "envoryx.db"))
	c.BackupsDir = env("ENVORYX_BACKUPS_DIR", defaultBackupsDir(c.ConfigDir))

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	var errs []error
	if !filepath.IsAbs(c.ConfigDir) {
		errs = append(errs, fmt.Errorf("ENVORYX_CONFIG_DIR must be absolute, got %q", c.ConfigDir))
	}
	if !filepath.IsAbs(c.ProjectsDir) {
		errs = append(errs, fmt.Errorf("ENVORYX_PROJECTS_DIR must be absolute, got %q", c.ProjectsDir))
	}
	if !filepath.IsAbs(c.BackupsDir) {
		errs = append(errs, fmt.Errorf("ENVORYX_BACKUPS_DIR must be absolute, got %q", c.BackupsDir))
	}
	if c.ConfigHostPath != "" && !filepath.IsAbs(c.ConfigHostPath) {
		errs = append(errs, fmt.Errorf("ENVORYX_CONFIG_HOST_PATH must be absolute, got %q", c.ConfigHostPath))
	}
	if c.ProjectsHostPath != "" && !filepath.IsAbs(c.ProjectsHostPath) {
		errs = append(errs, fmt.Errorf("ENVORYX_PROJECTS_HOST_PATH must be absolute, got %q", c.ProjectsHostPath))
	}
	if c.PortRangeStart < 1024 || c.PortRangeEnd > 65535 || c.PortRangeStart > c.PortRangeEnd {
		errs = append(errs, fmt.Errorf("invalid port range %d-%d", c.PortRangeStart, c.PortRangeEnd))
	}
	if c.PUID < 0 || c.PGID < 0 {
		errs = append(errs, errors.New("PUID/PGID must not be negative"))
	}
	if c.SessionIdleTimeout < time.Minute || c.SessionAbsoluteTimeout < time.Minute {
		errs = append(errs, errors.New("session timeouts must be at least 1m"))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("invalid ENVORYX_LOG_LEVEL %q", c.LogLevel))
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("invalid ENVORYX_LOG_FORMAT %q", c.LogFormat))
	}
	if c.PublicHost != "" && !validHost(c.PublicHost) {
		errs = append(errs, fmt.Errorf("ENVORYX_PUBLIC_HOST %q must be a host name or IP without scheme or port", c.PublicHost))
	}
	if (c.AdminUser == "") != (c.AdminPassword == "") {
		errs = append(errs, errors.New("ENVORYX_ADMIN_USER and ENVORYX_ADMIN_PASSWORD must be set together"))
	}
	return errors.Join(errs...)
}

var hostRe = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*$`)

// validHost accepts a DNS host name, an IPv4 or a bracket-free IPv6 address.
func validHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	return hostRe.MatchString(h)
}

// ValidHost is exported for the settings API.
func ValidHost(h string) bool { return validHost(h) }

// envAllowEmpty is like env but an explicitly empty value disables the feature.
func envAllowEmpty(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := env(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// mountedBackupsDir is picked up automatically when it exists, so that on Unraid adding
// the optional "/backups" path mapping is enough to move backups off the appdata share.
const mountedBackupsDir = "/backups"

func defaultBackupsDir(configDir string) string {
	if st, err := os.Stat(mountedBackupsDir); err == nil && st.IsDir() {
		return mountedBackupsDir
	}
	return filepath.Join(configDir, "backups")
}
