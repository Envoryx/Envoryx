// Staqio - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only
//
// Command staqio runs the Staqio server.
//
// Usage:
//
//	staqio             run the server (default)
//	staqio serve       run the server
//	staqio healthcheck probe the local server (used by the Docker HEALTHCHECK)
//	staqio version     print the version
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seramos/staqio/internal/acme"
	"github.com/seramos/staqio/internal/api"
	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/config"
	"github.com/seramos/staqio/internal/db"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/hostpath"
	"github.com/seramos/staqio/internal/mcpserver"
	"github.com/seramos/staqio/internal/notify"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/proxy"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/server"
	"github.com/seramos/staqio/internal/stats"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/tlsca"
	"github.com/seramos/staqio/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "staqio:", err)
			os.Exit(1)
		}
	case "healthcheck":
		os.Exit(healthcheck())
	case "version":
		fmt.Println("Staqio", version)
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
	log.Info("starting Staqio", "version", version, "config_dir", cfg.ConfigDir, "projects_dir", cfg.ProjectsDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, dir := range []string{cfg.ConfigDir, cfg.ProjectsDir, filepath.Join(cfg.ConfigDir, "projects")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// 1. Database.
	sqlDB, err := db.Open(ctx, cfg.DatabasePath, log)
	if err != nil {
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
		log.Warn("docker engine not reachable at startup; Staqio keeps running and retries on demand", "err", pingErr)
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
			log.Warn("could not auto-detect host paths; set STAQIO_PROJECTS_HOST_PATH and STAQIO_CONFIG_HOST_PATH", "err", err)
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
			ProjectsDir: cfg.ProjectsDir, ProjectsHostDir: projectsHost,
			PUID: cfg.PUID, PGID: cfg.PGID, StaqioVersion: version,
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
	go manager.RunReconciler(ctx, 30*time.Second, log)
	go func() {
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
	}()

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
			go acmeMgr.Run(ctx)
		}
	}
	proxyInfo := detectProxy(ctx, cfg, engine, resolver, certs != nil, log)

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
		MCP: mcpSrv.Handler(), Log: log, StartedAt: time.Now(),
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
			staqioURL := "http://" + project.UIHostname(base)
			if certs != nil && proxyInfo.HTTPSPort > 0 {
				staqioURL = "https://" + project.UIHostname(base)
				if proxyInfo.HTTPSPort != 443 {
					staqioURL += fmt.Sprintf(":%d", proxyInfo.HTTPSPort)
				}
			} else if proxyInfo.HTTPPort > 0 && proxyInfo.HTTPPort != 80 {
				staqioURL += fmt.Sprintf(":%d", proxyInfo.HTTPPort)
			}
			return manager.RouteTable(ctx, project.ProxyOptions{HTTPSPort: proxyInfo.HTTPSPort, StaqioURL: staqioURL, ExtraUIHosts: []string{publicHost(ctx)}})
		}
		router := proxy.NewRouter(source, 2*time.Second, log)
		proxyInfo.Invalidate = router.Invalidate
		handler := proxy.NewHandler(router, srv.Handler(), certs != nil, log)
		httpsAddr := ""
		if certs != nil {
			httpsAddr = cfg.ProxyHTTPS
		}
		ps := proxy.NewServer(handler, router, certs, cfg.ProxyHTTP, httpsAddr, nil, log)
		go func() {
			if err := ps.Run(ctx); err != nil {
				log.Warn("embedded proxy disabled", "err", err)
			}
		}()
	}

	if notifier != nil {
		notifier.Notify(ctx, notify.Event{Kind: "staqio.started", Level: notify.Info, Title: "Staqio started", Message: "Version " + version + " is up."})
	}
	if err := srv.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	log.Info("Staqio stopped")
	return nil
}

// detectProxy figures out how the proxy's listeners are reachable from the host: inside
// Docker from the published port bindings of Staqio's own container, on bare metal from
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
		log.Warn("could not read port bindings of the Staqio container", "err", err)
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
			log.Warn("could not inspect the Staqio container's network", "err", err)
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
	addr := os.Getenv("STAQIO_LISTEN")
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
