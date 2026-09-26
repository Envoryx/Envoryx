package project

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// hostGatewayEntry lets a container reach the Docker host as host.docker.internal, where
// an external server may run (Docker Desktop resolves the name itself, Linux does not).
const hostGatewayEntry = runtime.HostGateway + ":host-gateway"

// dbEnd is one database as a client reaches it: the project's own container, or an
// external server.
type dbEnd struct {
	p   store.Project
	svc *store.ProjectService
	cfg runtime.DatabaseConfig
}

// externalService reports whether a project service is a server Envoryx does not run.
func externalService(svc *store.ProjectService) bool {
	if svc == nil {
		return false
	}
	if svc.Kind.IsDatabase() {
		var cfg runtime.DatabaseConfig
		return json.Unmarshal(svc.Config, &cfg) == nil && cfg.External()
	}
	if svc.Kind == store.ServiceRedis {
		var cfg runtime.ServiceConfig
		return json.Unmarshal(svc.Config, &cfg) == nil && cfg.External()
	}
	return false
}

// clientSpec is the transient container a client runs in against an external server:
// the database image (its clients match the server version the user picked), no
// project network – the server is outside it – and the host reachable by name.
func clientSpec(e dbEnd, argv, env []string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:       fmt.Sprintf("envoryx-%s-dbclient-%d", e.p.Slug, time.Now().UnixNano()%1_000_000),
		Image:      e.svc.Image,
		Labels:     docker.ManagedLabels(e.p.ID, e.p.Slug, "dbclient", ""),
		Entrypoint: argv[:1],
		Cmd:        argv[1:],
		Env:        env,
		ExtraHosts: []string{hostGatewayEntry},
	}
}

// dbStream runs a dump or restore client (argv from the dialect) against a database with
// streamed input and output: an exec in its container, or a transient client container
// for an external server.
func (m *Manager) dbStream(ctx context.Context, e dbEnd, argv, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if e.cfg.External() {
		if err := m.engine.EnsureImage(ctx, e.svc.Image, nil); err != nil {
			return -1, fmt.Errorf("pull %s: %w", e.svc.Image, err)
		}
		return m.engine.RunOneShotStream(ctx, clientSpec(e, argv, env), docker.ExecStreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
	}
	c, err := m.ServiceContainer(ctx, e.p.ID, e.svc.Kind)
	if err != nil {
		return -1, err
	}
	if c.State != "running" {
		return -1, fmt.Errorf("%w: the database container must be running", ErrConflict)
	}
	return m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{Cmd: argv, Env: env, Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// runExternalSQL runs one statement against an external server in a transient client
// container.
func (m *Manager) runExternalSQL(ctx context.Context, e dbEnd, argv, env []string) (docker.ExecResult, error) {
	if err := m.engine.EnsureImage(ctx, e.svc.Image, nil); err != nil {
		return docker.ExecResult{}, fmt.Errorf("pull %s: %w", e.svc.Image, err)
	}
	return m.engine.RunOneShot(ctx, clientSpec(e, argv, env))
}

// externalUnreachable words a failed connection to an external server so it says where
// Envoryx tried to go.
func externalUnreachable(cfg runtime.DatabaseConfig, err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "database command failed: ")
	return fmt.Errorf("%w: cannot use the external database at %s:%d: %s", ErrConflict, cfg.Host, cfg.Port, msg)
}

// errExternalPort refuses publishing a port for a server Envoryx does not run.
var errExternalPort = fmt.Errorf("%w: an external database has no port of Envoryx's to publish; connect to the server itself", validate.ErrInvalid)

func derefDB(d *DatabaseRequest) DatabaseRequest {
	if d == nil {
		return DatabaseRequest{}
	}
	return *d
}

func namedDBs(list []NamedDatabaseRequest) []DatabaseRequest {
	out := make([]DatabaseRequest, len(list))
	for i, d := range list {
		out[i] = d.DatabaseRequest
	}
	return out
}

// checkExternalDatabase connects to an external server the way the project will: it must
// answer, accept the login and show the database. Called before a connection is stored,
// so a typo is reported where it was made.
func (m *Manager) checkExternalDatabase(ctx context.Context, p store.Project, svc *store.ProjectService) error {
	var cfg runtime.DatabaseConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return err
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		return err
	}
	step(ctx, "Connecting to the external database at {{address}}", "address", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	out, err := m.runSQL(ctx, p, svc, cfg, dialect.ListDatabases)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "database command failed: ")
		return fmt.Errorf("%w: cannot connect to %s at %s:%d as %s: %s", validate.ErrInvalid, svc.Variant, cfg.Host, cfg.Port, cfg.Username, msg)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == cfg.Database {
			return nil
		}
	}
	return fmt.Errorf("%w: %s at %s:%d has no database %q that %s can see", validate.ErrInvalid, svc.Variant, cfg.Host, cfg.Port, cfg.Database, cfg.Username)
}

// errExternalRedisPort refuses publishing a port for an external Redis.
var errExternalRedisPort = fmt.Errorf("%w: an external Redis has no port of Envoryx's to publish; connect to the server itself", validate.ErrInvalid)

// setExternalRedis stores an external Redis's address in the service; an empty password
// keeps keep (the stored one on an update).
func setExternalRedis(svc *store.ProjectService, e ExternalRedis, keep string) error {
	return editConfig(svc, func(c *runtime.ServiceConfig) error {
		*c = runtime.ServiceConfig{Host: e.Host, Port: e.Port, Password: e.Password}
		if c.Password == "" {
			c.Password = keep
		}
		return runtime.NormalizeExternalRedis(c)
	})
}

// checkExternalRedis pings an external Redis with the project's password from a client
// container of the Redis image.
func (m *Manager) checkExternalRedis(ctx context.Context, p store.Project, svc *store.ProjectService) error {
	var cfg runtime.ServiceConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return err
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	step(ctx, "Connecting to the external Redis at {{address}}", "address", addr)
	var env []string
	if cfg.Password != "" {
		env = []string{"REDISCLI_AUTH=" + cfg.Password} // never on the command line
	}
	res, err := m.runExternalSQL(ctx, dbEnd{p: p, svc: svc}, []string{"redis-cli", "-h", cfg.Host, "-p", strconv.Itoa(cfg.Port), "--no-auth-warning", "ping"}, env)
	if err != nil {
		return err
	}
	out := strings.Join(strings.Fields(res.Stdout+" "+res.Stderr), " ")
	if res.ExitCode != 0 || !strings.HasPrefix(out, "PONG") {
		if cfg.Password != "" {
			out = strings.ReplaceAll(out, cfg.Password, "***")
		}
		return fmt.Errorf("%w: cannot connect to Redis at %s: %s", validate.ErrInvalid, addr, out)
	}
	return nil
}

// ExternalTest is a connection to try before it is stored: Kind "database" (Type, Version
// pick the flavour and client) or "redis".
type ExternalTest struct {
	Kind     string
	Type     string
	Version  string
	Host     string
	Port     int
	Username string
	Password string
	Database string
}

// TestExternal connects to an external server the way a project would, without a
// project: the wizard's and the database tab's "Test connection".
func (m *Manager) TestExternal(ctx context.Context, t ExternalTest) error {
	probe := store.Project{ID: "connection-test", Slug: "connection-test"}
	switch t.Kind {
	case "database":
		svc, err := m.buildDatabaseService(probe.Slug, t.Type, t.Version, &ExternalDatabase{Host: t.Host, Port: t.Port, Username: t.Username, Password: t.Password, Database: t.Database})
		if err != nil {
			return err
		}
		return m.checkExternalDatabase(ctx, probe, &svc)
	case "redis":
		svc, err := m.buildExtraService(store.ServiceRedis, t.Version)
		if err != nil {
			return err
		}
		if err := setExternalRedis(&svc, ExternalRedis{Host: t.Host, Port: t.Port, Password: t.Password}, ""); err != nil {
			return err
		}
		return m.checkExternalRedis(ctx, probe, &svc)
	}
	return fmt.Errorf("%w: kind must be database or redis", validate.ErrInvalid)
}
