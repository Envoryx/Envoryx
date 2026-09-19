// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only
//
// Command envoryx runs the Envoryx server.
//
// Usage:
//
//	envoryx             run the server (default)
//	envoryx serve       run the server
//	envoryx healthcheck probe the local server (used by the Docker HEALTHCHECK)
//	envoryx version     print the version
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/envoryx/envoryx/internal/acme"
	"github.com/envoryx/envoryx/internal/api"
	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/hostpath"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/mcpserver"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/server"
	"github.com/envoryx/envoryx/internal/sshd"
	"github.com/envoryx/envoryx/internal/stats"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/tlsca"
	"github.com/envoryx/envoryx/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// errRestart is returned by serve when the process should start over (instance restore).
var errRestart = errors.New("restart requested")

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		err := serve()
		if errors.Is(err, errRestart) {
			// Replace the process instead of exiting: the container keeps running whatever
			// its restart policy, and the new process starts with a clean state.
			exe, lookErr := os.Executable()
			if lookErr == nil {
				err = syscall.Exec(exe, os.Args, os.Environ())
			} else {
				err = lookErr
			}
			fmt.Fprintln(os.Stderr, "envoryx: restart failed:", err)
			os.Exit(3)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "envoryx:", err)
			notifyStartFailure(err)
			os.Exit(1)
		}
	case "healthcheck":
		os.Exit(healthcheck())
	case "version":
		fmt.Println("Envoryx", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" || cfg.DevMode {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting Envoryx", "version", version, "config_dir", cfg.ConfigDir, "projects_dir", cfg.ProjectsDir, "backups_dir", cfg.BackupsDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := context.WithCancel(ctx)
	defer shutdown()
	var restart atomic.Bool
	requestRestart := func() {
		if restart.CompareAndSwap(false, true) {
			log.Info("restart requested")
			shutdown()
		}
	}

	for _, dir := range []string{cfg.ConfigDir, cfg.ProjectsDir, filepath.Join(cfg.ConfigDir, "projects"), cfg.BackupsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	warnStrandedBackups(cfg, log)
	var warnings []string
	for _, dir := range uniqueDirs(cfg.ConfigDir, filepath.Dir(cfg.DatabasePath)) {
		w, err := config.ValidateStorage(dir, cfg.AllowNetworkFS)
		if err != nil {
			return fmt.Errorf("storage: %w", err)
		}
		if w != "" {
			log.Warn("storage check", "dir", dir, "warning", w)
			warnings = append(warnings, w)
		}
	}

	// 1. Database. A scheduled instance restore replaces it first; a schema upgrade is
	// preceded by an automatic instance backup so the previous state can be brought back.
	backups := &instance.Store{ConfigDir: cfg.ConfigDir, DBPath: cfg.DatabasePath, Dir: filepath.Join(cfg.BackupsDir, "_instance"),
		Version: version, LatestSchema: db.LatestVersion(), Log: log}
	openRaw := func(ctx context.Context, path string) (*sql.DB, error) { return db.OpenRaw(ctx, path, log) }
	if restored, err := backups.ApplyPendingRestore(ctx, openRaw); err != nil {
		return fmt.Errorf("instance restore: %w", err)
	} else if restored != "" {
		log.Info("instance backup restored", "id", restored)
	}
	sqlDB, err := db.OpenWith(ctx, cfg.DatabasePath, log, db.Options{BeforeMigrate: func(ctx context.Context, raw *sql.DB, from, to int) error {
		b, err := backups.Create(ctx, raw, instance.KindPreMigrate, fmt.Sprintf("before schema %d → %d (Envoryx %s)", from, to, version))
		if err != nil {
			return err
		}
		log.Info("instance backup written before schema upgrade", "id", b.ID, "from", from, "to", to)
		return nil
	}})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCorrupt):
			return fmt.Errorf("database: %w – restore an instance backup (%s; see DEPLOYMENT.md, Instance backups)", err, newestBackupHint(backups, ""))
		case errors.Is(err, db.ErrNewerSchema):
			return fmt.Errorf("database: %w – either run the newer Envoryx image again, or restore the instance backup taken before its migration (%s; see DEPLOYMENT.md, Updating)", err, newestBackupHint(backups, instance.KindPreMigrate))
		}
		return fmt.Errorf("database: %w", err)
	}
	defer sqlDB.Close()
	st := store.New(sqlDB)
	schema, _ := db.SchemaVersion(ctx, sqlDB)
	log.Info("database ready", "path", cfg.DatabasePath, "schema", schema)

	// 2. Docker.
	engine, err := docker.Connect(docker.Options{Host: cfg.DockerHost}, log)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer engine.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	info, pingErr := engine.Ping(pingCtx)
	cancel()
	if pingErr != nil {
		log.Warn("docker engine not reachable at startup; Envoryx keeps running and retries on demand", "err", pingErr)
	} else {
		log.Info("docker engine connected", "api", info.APIVersion, "server", info.ServerVersion, "os", info.OS)
	}

	// 3. Host path detection for bind mounts.
	resolver := hostpath.New(engine, map[string]string{
		cfg.ConfigDir:   cfg.ConfigHostPath,
		cfg.ProjectsDir: cfg.ProjectsHostPath,
	})
	if err := resolver.Detect(ctx); err != nil {
		if cfg.ConfigHostPath == "" || cfg.ProjectsHostPath == "" {
			log.Warn("could not auto-detect host paths; set ENVORYX_PROJECTS_HOST_PATH and ENVORYX_CONFIG_HOST_PATH", "err", err)
		}
	}
	logHostPaths(log, resolver, cfg)

	// 4. Services.
	auditLog := audit.New(st.Audit, log)
	sessions := auth.NewService(st, auth.Options{
		IdleTimeout:     cfg.SessionIdleTimeout,
		AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
		SecureCookies:   cfg.SecureCookies,
	}, log)
	if err := bootstrapAdmin(ctx, cfg, sessions, auditLog, log); err != nil {
		return err
	}

	catalog := runtime.Default()
	paths := func() (project.Paths, error) {
		resolver.EnsureDetected(ctx)
		projectsHost, err := resolver.Resolve(cfg.ProjectsDir)
		if err != nil {
			return project.Paths{}, err
		}
		configHost, err := resolver.Resolve(cfg.ConfigDir)
		if err != nil {
			return project.Paths{}, err
		}
		return project.Paths{
			ConfigDir: cfg.ConfigDir, ConfigHostDir: configHost,
			ProjectsDir: cfg.ProjectsDir, ProjectsHostDir: projectsHost, BackupsDir: cfg.BackupsDir,
			PUID: cfg.PUID, PGID: cfg.PGID, EnvoryxVersion: version,
			SelfContainerID: resolver.SelfContainerID(),
		}, nil
	}
	manager := project.NewManager(st, engine, catalog, paths, auditLog, project.Config{
		PortRangeStart: cfg.PortRangeStart, PortRangeEnd: cfg.PortRangeEnd, StopTimeout: 10 * time.Second,
	}, log)
	collector := stats.New(engine, 5*time.Second, log)
	notifier, err := notify.New(cfg.ConfigDir, log)
	if err != nil {
		log.Warn("notifications unavailable", "err", err)
	} else {
		manager.SetNotifier(notifier)
	}

	// 5. Reconcile desired vs. actual state, then keep doing so in the background.
	background := func(name string, fn func(context.Context)) { go supervise(ctx, log, notifier, name, fn) }
	background("reconciler", func(ctx context.Context) { manager.RunReconciler(ctx, 30*time.Second, log) })
	background("backup scheduler", func(ctx context.Context) { manager.RunBackupScheduler(ctx, time.Minute, log) })
	background("session purge", func(ctx context.Context) {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			sessions.PurgeExpired(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	})

	// 6. Local CA for the embedded proxy.
	var certs *tlsca.Store
	if cfg.ProxyHTTPS != "" {
		certs, err = tlsca.Open(filepath.Join(cfg.ConfigDir, "ca"))
		if err != nil {
			log.Warn("local certificate authority unavailable; HTTPS for projects is disabled", "err", err)
		}
	}
	var acmeMgr *acme.Manager
	if certs != nil {
		acmeMgr, err = acme.New(filepath.Join(cfg.ConfigDir, "ca"), certs, log)
		if err != nil {
			log.Warn("Let's Encrypt integration unavailable", "err", err)
		} else {
			if notifier != nil {
				acmeMgr.SetNotifier(notifier)
			}
			background("certificate renewal", acmeMgr.Run)
		}
	}
	proxyInfo := detectProxy(ctx, cfg, engine, resolver, certs != nil, log)

	// SSH for IDE remote interpreters / SFTP into project containers.
	var sshInfo *api.SSHInfo
	if cfg.SSHListen != "" {
		sshSrv, err := sshd.New(sshd.Deps{Auth: sessions, Store: st, Projects: manager, Engine: engine, Audit: auditLog, HostKeyPath: filepath.Join(cfg.ConfigDir, "ssh", "host_ed25519"), Log: log})
		if err != nil {
			log.Warn("ssh server unavailable", "err", err)
		} else {
			sshInfo = &api.SSHInfo{Enabled: true, Port: portOfAddr(cfg.SSHListen), Fingerprint: sshSrv.Fingerprint()}
			if proxyInfo.InDocker && proxyInfo.Address == "" {
				if bindings, err := engine.PortBindings(ctx, resolver.SelfContainerID()); err == nil {
					sshInfo.Port = 0
					for _, b := range bindings {
						if b.ContainerPort == portOfAddr(cfg.SSHListen) && (b.Protocol == "" || b.Protocol == "tcp") {
							sshInfo.Port = b.HostPort
						}
					}
				}
			}
			background("ssh server", func(ctx context.Context) {
				if err := sshSrv.Run(ctx, cfg.SSHListen); err != nil {
					log.Warn("ssh server stopped", "err", err)
				}
			})
		}
	}

	// 7. HTTP + MCP.
	dist, err := web.Dist()
	if err != nil {
		log.Warn("embedded frontend unavailable", "err", err)
	}
	publicHost := func(ctx context.Context) string {
		if v, err := st.Settings.Get(ctx, api.SettingPublicHost); err == nil {
			return v
		}
		return cfg.PublicHost
	}
	mcpLinks := mcpserver.Links{PublicHost: publicHost}
	if certs != nil {
		mcpLinks.HTTPSPort = proxyInfo.HTTPSPort
	}
	mcpLinks.HTTPPort = proxyInfo.HTTPPort
	mcpSrv := mcpserver.New(mcpserver.Deps{Projects: manager, Catalog: catalog, Auth: sessions, Links: mcpLinks, Version: version, Log: log})
	a := api.New(api.Deps{
		Config: cfg, Version: version, Store: st, Auth: sessions, Audit: auditLog, Engine: engine,
		Projects: manager, Catalog: catalog, Stats: collector, HostPath: resolver, Certs: certs, ACME: acmeMgr, Notify: notifier, Proxy: proxyInfo,
		MCP: mcpSrv.Handler(), SSH: sshInfo, Log: log, StartedAt: time.Now(),
		Instance: backups, DB: sqlDB, Restart: requestRestart, Warnings: warnings,
	})
	var origins []string
	if cfg.DevMode {
		origins = append(origins, cfg.DevOrigin)
		if u, err := url.Parse(cfg.DevOrigin); err == nil {
			a.SetAllowedOriginHosts([]string{u.Host})
		}
	}
	srv := server.New(server.Options{Addr: cfg.ListenAddr, AllowedOrigins: origins, Log: log, MCP: mcpSrv.Handler()}, a, sessions, dist)

	// 8. Embedded reverse proxy (host-name routing + HTTPS for projects and the UI).
	if proxyInfo.Enabled {
		source := func(ctx context.Context) (proxy.Table, error) {
			base := manager.BaseDomain(ctx)
			envoryxURL := "http://" + project.UIHostname(base)
			if certs != nil && proxyInfo.HTTPSPort > 0 {
				envoryxURL = "https://" + project.UIHostname(base)
				if proxyInfo.HTTPSPort != 443 {
					envoryxURL += fmt.Sprintf(":%d", proxyInfo.HTTPSPort)
				}
			} else if proxyInfo.HTTPPort > 0 && proxyInfo.HTTPPort != 80 {
				envoryxURL += fmt.Sprintf(":%d", proxyInfo.HTTPPort)
			}
			return manager.RouteTable(ctx, project.ProxyOptions{HTTPSPort: proxyInfo.HTTPSPort, EnvoryxURL: envoryxURL, ExtraUIHosts: []string{publicHost(ctx)}})
		}
		router := proxy.NewRouter(source, 2*time.Second, log)
		proxyInfo.Invalidate = router.Invalidate
		handler := proxy.NewHandler(router, srv.Handler(), certs != nil, log)
		httpsAddr := ""
		if certs != nil {
			httpsAddr = cfg.ProxyHTTPS
		}
		ps := proxy.NewServer(handler, router, certs, cfg.ProxyHTTP, httpsAddr, nil, log)
		background("proxy", func(ctx context.Context) {
			if err := ps.Run(ctx); err != nil {
				log.Warn("embedded proxy disabled", "err", err)
			}
		})
	}

	if notifier != nil {
		notifier.Notify(ctx, notify.Event{Kind: "envoryx.started", Level: notify.Info, Title: "Envoryx started", Message: "Version " + version + " is up."})
	}
	if err := srv.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	if restart.Load() {
		log.Info("Envoryx restarting")
		return errRestart
	}
	log.Info("Envoryx stopped")
	return nil
}

// supervise runs a background task and keeps it alive: a panic is logged with its stack,
// reported through notifications and the task is started again with backoff. A task
// that returns on its own (context done, listener failed) is left alone.
func supervise(ctx context.Context, log *slog.Logger, notifier *notify.Service, name string, fn func(context.Context)) {
	backoff := time.Second
	for {
		if runGuarded(ctx, log, notifier, name, fn) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// runGuarded reports true when fn returned normally, false when it panicked.
func runGuarded(ctx context.Context, log *slog.Logger, notifier *notify.Service, name string, fn func(context.Context)) (ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			ok = false
			log.Error("background task crashed; restarting it", "task", name, "panic", rec, "stack", string(debug.Stack()))
			if notifier != nil {
				notifier.Notify(ctx, notify.Event{Kind: "envoryx.failed", Level: notify.Error, Title: "Envoryx: " + name + " crashed",
					Message: fmt.Sprintf("%v\nThe task was restarted automatically; see the container log for the stack trace.", rec)})
			}
		}
	}()
	fn(ctx)
	return true
}

// notifyStartFailure tells the operator that Envoryx refused to start. It needs no
// database: notification settings live in a file in the config directory.
func notifyStartFailure(cause error) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	notifier, err := notify.New(cfg.ConfigDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := notifier.NotifySync(ctx, notify.Event{Kind: "envoryx.failed", Level: notify.Error, Title: "Envoryx failed to start", Message: cause.Error()}); err != nil {
		fmt.Fprintln(os.Stderr, "envoryx: start-failure notification not delivered:", err)
	}
}

// newestBackupHint names the newest instance backup (of a kind, "" = any) for error messages.
func newestBackupHint(backups *instance.Store, kind string) string {
	list, err := backups.List()
	if err != nil {
		return "no instance backup found"
	}
	for _, b := range list {
		if kind == "" || b.Kind == kind {
			return fmt.Sprintf("the newest %s backup is %s in %s", b.Kind, b.ID, backups.Dir)
		}
	}
	return "no instance backup found"
}

// uniqueDirs drops duplicates, keeping order.
func uniqueDirs(dirs ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// portOfAddr extracts the port of a listen address ("" → 0).
func portOfAddr(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(port, "%d", &n)
	return n
}

// detectProxy figures out how the proxy's listeners are reachable from the host: inside
// Docker from the published port bindings of Envoryx's own container, on bare metal from
// the configured listen addresses.
func detectProxy(ctx context.Context, cfg config.Config, engine docker.Engine, resolver *hostpath.Resolver, tlsEnabled bool, log *slog.Logger) *api.ProxyInfo {
	info := &api.ProxyInfo{Enabled: cfg.ProxyHTTP != "" || (cfg.ProxyHTTPS != "" && tlsEnabled)}
	if !info.Enabled {
		return info
	}
	portOf := func(addr string) int {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return 0
		}
		var n int
		fmt.Sscanf(port, "%d", &n)
		return n
	}
	httpPort, httpsPort := portOf(cfg.ProxyHTTP), 0
	if tlsEnabled {
		httpsPort = portOf(cfg.ProxyHTTPS)
	}
	selfID := resolver.SelfContainerID()
	if selfID == "" {
		info.HTTPPort, info.HTTPSPort = httpPort, httpsPort
		return info
	}
	info.InDocker = true
	bindings, err := engine.PortBindings(ctx, selfID)
	if err != nil {
		log.Warn("could not read port bindings of the Envoryx container", "err", err)
		return info
	}
	for _, b := range bindings {
		if b.Protocol != "" && b.Protocol != "tcp" {
			continue
		}
		switch {
		case b.ContainerPort == httpPort && httpPort != 0:
			info.HTTPPort = b.HostPort
		case b.ContainerPort == httpsPort && httpsPort != 0:
			info.HTTPSPort = b.HostPort
		}
	}
	if info.HTTPPort == 0 && info.HTTPSPort == 0 {
		// No published ports: with host networking or an own IP (macvlan/ipvlan, Unraid
		// "br0") the listeners are reachable directly.
		access, err := engine.NetworkAccess(ctx, selfID)
		if err != nil {
			log.Warn("could not inspect the Envoryx container's network", "err", err)
		} else if access.Direct {
			info.HTTPPort, info.HTTPSPort = httpPort, httpsPort
			if len(access.IPs) > 0 {
				info.Address = access.IPs[0]
			}
			log.Info("proxy reachable directly on the container's own address", "mode", access.Mode, "ips", access.IPs)
		} else {
			log.Warn("proxy ports are not published; map host ports to the container's proxy ports to use domains", "http", cfg.ProxyHTTP, "https", cfg.ProxyHTTPS)
		}
	}
	return info
}

func logHostPaths(log *slog.Logger, r *hostpath.Resolver, cfg config.Config) {
	for _, dir := range []string{cfg.ProjectsDir, cfg.ConfigDir} {
		host, err := r.Resolve(dir)
		if err != nil {
			log.Warn("host path unresolved; project creation is disabled until fixed", "container_path", dir, "err", err)
			continue
		}
		log.Info("host path resolved", "container_path", dir, "host_path", host)
	}
}

// bootstrapAdmin creates the first admin from the environment if configured and no user exists.
func bootstrapAdmin(ctx context.Context, cfg config.Config, sessions *auth.Service, auditLog *audit.Logger, log *slog.Logger) error {
	needs, err := sessions.NeedsSetup(ctx)
	if err != nil {
		return err
	}
	if !needs {
		return nil
	}
	if cfg.AdminUser == "" {
		log.Info("no user configured yet; open the web UI to create the admin account")
		return nil
	}
	user, err := sessions.CreateInitialAdmin(ctx, cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin user: %w", err)
	}
	auditLog.LogAs(ctx, user.Username, audit.ActionSetup, "user", user.ID, map[string]string{"source": "environment"})
	log.Info("admin user created from environment", "username", user.Username)
	return nil
}

// healthcheck probes the local HTTP server. It is used by the container HEALTHCHECK so
// the image needs no curl/wget.
func healthcheck() int {
	addr := os.Getenv("ENVORYX_LISTEN")
	if addr == "" {
		addr = ":8787"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = strings.TrimPrefix(addr, ":")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/api/v1/health", net.JoinHostPort(host, port)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}

// warnStrandedBackups points out backups that were left in the old default location
// after the backups directory was moved; they are invisible to Envoryx until moved.
func warnStrandedBackups(cfg config.Config, log *slog.Logger) {
	old := filepath.Join(cfg.ConfigDir, "backups")
	if old == cfg.BackupsDir {
		return
	}
	entries, err := os.ReadDir(old)
	if err != nil || len(entries) == 0 {
		return
	}
	log.Warn("backups directory moved but the old location is not empty; move its contents to the new directory to make those backups visible",
		"old", old, "new", cfg.BackupsDir)
}
