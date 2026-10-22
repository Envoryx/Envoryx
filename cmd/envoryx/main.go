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
//	envoryx admin …     rescue commands: list accounts, reset a password, end sessions,
//	                    revoke API tokens, reset all accounts (see admin.go)
//	envoryx version     print the version
//
// Everything else is the client side: login, logout, whoami, up, import, project,
// backup, db and git work against a running Envoryx over its REST API (see cli.go).
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
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/fleet"
	"github.com/envoryx/envoryx/internal/hostpath"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/mcpserver"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/oidc"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/provenance"
	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/selfupdate"
	"github.com/envoryx/envoryx/internal/server"
	"github.com/envoryx/envoryx/internal/sshd"
	"github.com/envoryx/envoryx/internal/stats"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/tlsca"
	"github.com/envoryx/envoryx/internal/update"
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
	case "self-update":
		os.Exit(selfUpdateHelper(os.Args[2:]))
	case "admin":
		os.Exit(adminCommand(os.Args[2:]))
	case "version":
		fmt.Println("Envoryx", version)
	case "help", "-h", "--help":
		fmt.Print(cliUsage)
	default:
		// Everything else is a client command talking to a server over the API. Ctrl+C
		// cancels the request instead of killing the process mid-stream.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		os.Exit(cliCommand(ctx, os.Args[1:]))
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
	// updating is set once a self-update hands over to its helper: the stop that follows
	// is a restart under a new version, not a shutdown the projects follow.
	var updating atomic.Bool
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

	// The key that seals the secrets at rest: ENVORYX_SECRET_KEY, else /config/secret.key
	// (created now if missing). Keys of a rotation or restore still open older values
	// until everything is resealed below.
	keyRing, keySource, err := secrets.Load(cfg.ConfigDir, os.Getenv)
	if err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	secrets.SetDefault(keyRing)
	if !keySource.Created {
		log.Info("secret key loaded", "source", keySource.Kind, "keyId", keyRing.Current().ID())
	}
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
	restored, err := backups.ApplyPendingRestore(ctx, openRaw)
	if err != nil {
		return fmt.Errorf("instance restore: %w", err)
	}
	if restored != nil {
		log.Info("instance backup restored", "id", restored.ID, "preRestore", restored.PreRestoreID, "requestedBy", restored.RequestedBy)
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
			return fmt.Errorf("database: %w - restore an instance backup (%s; see DEPLOYMENT.md, Instance backups)", err, newestBackupHint(backups, ""))
		case errors.Is(err, db.ErrNewerSchema):
			return fmt.Errorf("database: %w - either run the newer Envoryx image again, or restore the instance backup taken before its migration (%s; see DEPLOYMENT.md, Updating)", err, newestBackupHint(backups, instance.KindPreMigrate))
		}
		return fmt.Errorf("database: %w", err)
	}
	defer sqlDB.Close()
	st := store.New(sqlDB)
	if err := st.CheckSecretKey(ctx); err != nil {
		return fmt.Errorf("secret key: %w", err)
	}
	if keySource.Created {
		if err := keySource.Persist(); err != nil {
			return fmt.Errorf("secret key: %w", err)
		}
		log.Warn("created a new secret key: keep a copy (Settings, Secret key) or set ENVORYX_SECRET_KEY; without it an instance backup cannot be restored elsewhere", "path", keySource.Path, "keyId", keyRing.Current().ID())
	}
	schema, _ := db.SchemaVersion(ctx, sqlDB)
	log.Info("database ready", "path", cfg.DatabasePath, "schema", schema)

	// 2. Docker. The instance ID marks what this instance creates, so another Envoryx on
	// the same Docker host leaves it alone and vice versa.
	instanceID, created, err := instance.LoadID(cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("instance ID: %w", err)
	}
	if created {
		log.Info("created the instance ID", "id", instanceID, "path", filepath.Join(cfg.ConfigDir, instance.IDFile))
	}
	engine, err := docker.Connect(docker.Options{Host: cfg.DockerHost, Instance: instanceID}, log)
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
		log.Info("docker engine connected", "api", info.APIVersion, "server", info.ServerVersion, "os", info.OS, "instance", instanceID)
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
	if restored != nil && pingErr != nil && restored.InstanceID != "" && restored.InstanceID != instanceID {
		log.Warn("Docker is not reachable, so the restored backup's instance ID was not checked; if this Envoryx lost its /config and its project containers are still on the host, write that ID into the instance ID file", "backupInstanceId", restored.InstanceID, "file", filepath.Join(cfg.ConfigDir, instance.IDFile))
	}
	adoptedID := ""
	if restored != nil && pingErr == nil {
		if adoptedID = adoptRestoredInstanceID(ctx, cfg.ConfigDir, restored.InstanceID, instanceID, engine, st, resolver.SelfContainerID(), log); adoptedID != "" {
			instanceID = adoptedID
		}
	}

	// 4. Services.
	auditLog := audit.New(st.Audit, log)
	if restored != nil {
		// The restore_scheduled entry went into the database that was just replaced;
		// record the outcome in the restored one so the audit trail explains the jump.
		details := map[string]any{"preRestore": restored.PreRestoreID, "requestedAt": restored.RequestedAt}
		if adoptedID != "" {
			details["adoptedInstanceId"] = adoptedID
		}
		auditLog.LogAs(ctx, restored.RequestedBy, audit.ActionInstanceRestored, "instance", restored.ID, details)
	}
	sessions := auth.NewService(st, auth.Options{
		IdleTimeout:     cfg.SessionIdleTimeout,
		AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
		SecureCookies:   cfg.SecureCookies,
	}, log)
	if err := bootstrapAdmin(ctx, cfg, sessions, auditLog, log); err != nil {
		return err
	}
	// A hoster's plan limits a managed instance; without the file nothing is limited.
	plans, err := plan.Open(filepath.Join(cfg.ConfigDir, plan.File), log)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if pl := plans.Get(); pl != nil {
		log.Info("plan loaded", "name", pl.Name)
	}
	sessions.SetPlan(plans)

	catalog := runtime.Default()
	// The projects' host ports listen on every interface of a machine of one's own. On a
	// hoster's they stay on the machine itself: its customers reach the projects through
	// the proxy, and a database or debugger port must not face the internet.
	localPorts := func() bool {
		switch cfg.HostPorts {
		case "local":
			return true
		case "all":
			return false
		}
		_, err := os.Stat(filepath.Join(cfg.ConfigDir, fleet.StateFile))
		return plans.Get() != nil || cfg.FleetURL != "" || err == nil
	}
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
			SelfContainerID: resolver.SelfContainerID(), PublishInterface: publishInterface(localPorts()),
		}, nil
	}
	manager := project.NewManager(st, engine, catalog, paths, auditLog, project.Config{
		PortRangeStart: cfg.PortRangeStart, PortRangeEnd: cfg.PortRangeEnd, NetworkPool: cfg.NetworkPool, StopTimeout: 10 * time.Second,
		ConfigDir: cfg.ConfigDir, BackupsDir: cfg.BackupsDir, LocalPorts: localPorts,
	}, log)
	if localPorts() {
		log.Info("project ports listen on 127.0.0.1 only; set ENVORYX_HOST_PORTS=all to open them on every interface")
	}
	manager.SetSecretKeySource(keySource)
	manager.SetPlan(plans)
	// Plain values from before encryption and values of an older key get the current key;
	// only then are the key files of a rotation or restore gone.
	if rep, err := manager.ResealAtStart(ctx); err != nil {
		return fmt.Errorf("seal secrets: %w", err)
	} else if rep != (project.ResealReport{}) {
		log.Info("secrets sealed with the current key", "keyId", keyRing.Current().ID(), "database", rep.Database, "files", rep.Files, "backups", rep.Backups)
	}
	if removed := keySource.Cleanup(); len(removed) > 0 {
		log.Info("old key files removed after resealing", "files", removed)
	}
	keyRing.DropOld()
	collector := stats.New(engine, 5*time.Second, log)
	collector.Filter = manager.OwnContainers
	notifier, err := notify.New(cfg.ConfigDir, log)
	if err != nil {
		log.Warn("notifications unavailable", "err", err)
	} else {
		manager.SetNotifier(notifier)
	}

	// Offsite targets get scheduled project backups and a daily instance backup.
	var syncer *offsite.Syncer
	if offsiteCfg, err := offsite.OpenConfig(cfg.ConfigDir); err != nil {
		log.Warn("offsite backups unavailable", "err", err)
	} else {
		syncer = &offsite.Syncer{Config: offsiteCfg, Store: st, Projects: manager, Instance: backups, Log: log,
			CreateInstanceBackup: func(ctx context.Context, kind string) (instance.Info, error) {
				return backups.Create(ctx, sqlDB, kind, "daily, for the offsite targets")
			}}
		if notifier != nil {
			syncer.Notify = notifier
		}
		syncer.Allowed = func() bool { return plans.Get().Allows(plan.FeatureOffsite) }
		manager.SetBackupHook(syncer.OnProjectBackup)
	}

	// Local CA for the embedded proxy. Opened before the reconciler starts any project: the
	// containers that run project code trust it through the files in ca-trust, and a plan
	// made without them would recreate the containers again once they are there.
	var certs *tlsca.Store
	if cfg.ProxyHTTPS != "" {
		certs, err = tlsca.Open(filepath.Join(cfg.ConfigDir, "ca"))
		if err != nil {
			log.Warn("local certificate authority unavailable; HTTPS for projects is disabled", "err", err)
		} else if err := certs.WriteTrust(filepath.Join(cfg.ConfigDir, project.CATrustDir)); err != nil {
			log.Warn("project containers won't trust the local CA", "err", err)
		} else {
			manager.SetCATrust(true)
		}
	}

	// 5. Clear the remains of backups a crash interrupted, then reconcile desired vs.
	// actual state and keep doing so in the background.
	backups.Sweep()
	manager.SweepBackups(ctx)
	background := func(name string, fn func(context.Context)) { go supervise(ctx, log, notifier, name, fn) }
	background("reconciler", func(ctx context.Context) {
		// With "projects follow Envoryx" on, the projects that were running before the
		// stop come back first, so the first reconcile does not report them as down.
		if manager.ProjectsFollowEnvoryx(ctx) {
			manager.ResumeProjects(ctx, log)
		}
		manager.RunReconciler(ctx, 30*time.Second, log)
	})
	background("backup scheduler", func(ctx context.Context) { manager.RunBackupScheduler(ctx, time.Minute, log) })
	background("OOM watcher", func(ctx context.Context) { manager.RunOOMWatcher(ctx, log) })
	background("resource history", func(ctx context.Context) { manager.RunMetrics(ctx, time.Minute, log) })
	background("health checks", func(ctx context.Context) { manager.RunHealthChecks(ctx, 5*time.Second, log) })
	background("share janitor", func(ctx context.Context) { manager.RunShareJanitor(ctx, 30*time.Second, log) })
	background("audit retention", func(ctx context.Context) { manager.RunAuditRetention(ctx, time.Hour, log) })
	if syncer != nil {
		background("offsite backups", func(ctx context.Context) { syncer.Run(ctx, time.Minute) })
	}
	// Every 15 seconds: a job fires at most that long after its minute starts.
	background("cron scheduler", func(ctx context.Context) { manager.RunCronScheduler(ctx, 15*time.Second, log) })
	// Branch environments: repositories polled on their own interval, idle ones stopped.
	background("branch scheduler", func(ctx context.Context) { manager.RunBranchScheduler(ctx, 30*time.Second, log) })
	// Container output outlives the containers: followed as it comes, kept per day.
	if logStore, err := logs.OpenStore(filepath.Join(cfg.ConfigDir, "logs")); err != nil {
		log.Warn("log history unavailable; the Logs section reads the containers only", "err", err)
	} else {
		manager.SetLogStore(logStore)
		background("log history", func(ctx context.Context) { manager.RunLogHistory(ctx, 5*time.Second, log) })
	}
	// Daily: newer builds of the projects' images, and the restart that applies them.
	background("image updates", func(ctx context.Context) { manager.RunImageUpdates(ctx, log) })
	background("plan disk usage", func(ctx context.Context) { manager.RunDiskUsage(ctx, 5*time.Minute) })
	background("suspension", func(ctx context.Context) { manager.RunSuspension(ctx, 30*time.Second, log) })
	background("disk space monitor", func(ctx context.Context) {
		disk.Monitor(ctx, 5*time.Minute, notifier, log, cfg.ConfigDir, cfg.ProjectsDir, cfg.BackupsDir)
	})
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

	// 6. Let's Encrypt for the embedded proxy (the local CA is opened before the reconciler).
	var acmeMgr *acme.Manager
	if certs != nil {
		acmeMgr, err = acme.New(filepath.Join(cfg.ConfigDir, "ca"), certs, log)
		if err != nil {
			log.Warn("Let's Encrypt integration unavailable", "err", err)
		} else {
			if notifier != nil {
				acmeMgr.SetNotifier(notifier)
			}
		}
	}
	// The certificate a hoster's fleet manager provides takes the place of Let's Encrypt
	// and an own certificate while it is there. It is applied before the renewal starts,
	// which would otherwise get a certificate of its own first.
	var hosterCertErr atomic.Pointer[string]
	applyHosterCert := func(c *plan.Certificate) {
		hosterCertErr.Store(nil)
		var err error
		switch {
		case acmeMgr != nil:
			err = acmeMgr.SetHoster(c)
		case c != nil:
			err = errors.New("this instance serves no HTTPS, so it can't use the hoster's certificate")
		}
		if err != nil {
			log.Warn("the hoster's certificate is not used", "err", err)
			msg := err.Error()
			hosterCertErr.Store(&msg)
		}
	}
	applyHosterCert(plans.Certificate())
	plans.OnCertificate(applyHosterCert)
	if acmeMgr != nil {
		background("certificate renewal", acmeMgr.Run)
	}
	proxyInfo := detectProxy(ctx, cfg, engine, resolver, certs != nil, log)

	// SSH for IDE remote interpreters / SFTP into project containers.
	var sshInfo *api.SSHInfo
	if cfg.SSHListen != "" {
		sshSrv, err := sshd.New(sshd.Deps{Auth: sessions, Store: st, Projects: manager, Engine: engine, Audit: auditLog, HostKeyPath: filepath.Join(cfg.ConfigDir, "ssh", "host_ed25519"), Log: log})
		if err != nil {
			log.Warn("ssh server unavailable", "err", err)
		} else {
			sshInfo = &api.SSHInfo{Enabled: true, Port: portOfAddr(cfg.SSHListen), Fingerprint: sshSrv.Fingerprint(), FingerprintMD5: sshSrv.FingerprintMD5()}
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
	manager.SetLinks(func(ctx context.Context) (string, int, int) {
		return publicHost(ctx), mcpLinks.HTTPPort, mcpLinks.HTTPSPort
	})
	mcpSrv := mcpserver.New(mcpserver.Deps{Projects: manager, Catalog: catalog, Auth: sessions, Links: mcpLinks, Version: version, Log: log})
	updates := update.Disabled(version)
	if cfg.UpdateCheck {
		updates = update.New(version, "", log)
	}
	// planOffsiteErr is why the plan's offsite target isn't used; the manager hears it.
	var planOffsiteErr atomic.Pointer[string]
	// planSettingsErr names the plan's fixed settings the instance could not apply; the
	// manager hears it too.
	var planSettingsErr atomic.Pointer[string]
	// uiURL is the address of the web UI behind the embedded proxy (envoryx.<base>), or
	// the public host and listen port without it.
	uiURL := func(ctx context.Context) string {
		if !proxyInfo.Enabled {
			_, port, _ := net.SplitHostPort(cfg.ListenAddr)
			host := publicHost(ctx)
			if host == "" {
				host = "localhost"
			}
			return "http://" + net.JoinHostPort(host, port)
		}
		base := manager.BaseDomain(ctx)
		u := "http://" + project.UIHostname(base)
		if certs != nil && proxyInfo.HTTPSPort > 0 {
			u = "https://" + project.UIHostname(base)
			if proxyInfo.HTTPSPort != 443 {
				u += fmt.Sprintf(":%d", proxyInfo.HTTPSPort)
			}
		} else if proxyInfo.HTTPPort > 0 && proxyInfo.HTTPPort != 80 {
			u += fmt.Sprintf(":%d", proxyInfo.HTTPPort)
		}
		return u
	}
	// A hoster's fleet manager pushes the plan and gets the instance's state back; with
	// the admins' consent its support signs in through a one-time link.
	remote := &remoteBackups{backups: backups, db: sqlDB, st: st, syncer: syncer, log: log}
	updater := &selfupdate.Updater{Engine: engine, SelfID: resolver.SelfContainerID, ConfigDir: cfg.ConfigDir, Version: version, Log: log,
		BeforeStop: func() { updating.Store(true) }}
	if !cfg.UpdateUnverified {
		// Updates take only images the release workflow attested for that version.
		v := &provenance.Verifier{CacheDir: filepath.Join(cfg.ConfigDir, "sigstore")}
		updater.Verify = v.Verify
	}
	fleetAgent, err := fleet.New(fleet.Config{URL: cfg.FleetURL, Token: cfg.FleetToken, Insecure: cfg.FleetInsecure, ConfigDir: cfg.ConfigDir, Plan: plans, Log: log,
		Status: func(ctx context.Context) fleet.Status {
			s := fleetStatus(ctx, st, manager, version, instanceID)
			s.UIURL = uiURL(ctx)
			if msg := planOffsiteErr.Load(); msg != nil {
				s.Warnings = append(s.Warnings, *msg)
			}
			if msg := planSettingsErr.Load(); msg != nil {
				s.Warnings = append(s.Warnings, *msg)
			}
			if h := acmeMgr.HosterStatus(); h != nil {
				s.Certificate = &fleet.CertificateStatus{Domain: h.Domain, NotAfter: h.NotAfter}
			}
			if msg := hosterCertErr.Load(); msg != nil {
				if s.Certificate == nil {
					s.Certificate = &fleet.CertificateStatus{}
				}
				s.Certificate.Error = *msg
			}
			s.Backups = remote.Status(ctx)
			us := updater.Status(ctx)
			s.Update = &us
			if acc, err := sessions.SupportAccess(ctx); err == nil && acc.Allowed {
				s.Support = &fleet.SupportAccess{Until: acc.Until}
			}
			return s
		},
		SupportLogin: func(ctx context.Context, hoster string) (string, error) {
			token, err := sessions.IssueSupportLogin(ctx, hoster)
			if err != nil {
				return "", err
			}
			return uiURL(ctx) + "/api/v1/auth/support/" + token, nil
		},
		Backup: remote.Start,
		Update: updater.Start})
	if err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	remote.done = fleetAgent.Nudge
	if fleetAgent != nil {
		background("fleet", fleetAgent.Run)
	}
	// reportUpdate logs a finished self-update nobody logged yet and tells the fleet
	// manager; it reports whether it found one or can't look.
	reportUpdate := func(ctx context.Context) bool {
		r, err := selfupdate.TakeUnreported(cfg.ConfigDir)
		if err != nil {
			log.Warn("update: reading the last result", "err", err)
			return true
		}
		if r == nil {
			return false
		}
		action := audit.ActionInstanceUpdated
		if !r.OK {
			action = audit.ActionInstanceUpdateFailed
		}
		auditLog.Log(ctx, action, "instance", "", map[string]any{"from": r.From, "to": r.To, "error": r.Error, "rolledBack": r.RolledBack})
		return true
	}
	// An update that ends while this process runs failed: the old version stays, the
	// result is logged here and the fleet manager learns of it at once.
	updater.Failed = func() {
		updating.Store(false)
		reportUpdate(ctx)
		fleetAgent.Nudge()
	}
	// How a self-update went goes into the audit log of the version that runs afterwards.
	// The helper finishes only once this process is healthy, so it is looked for a while.
	background("update result", func(ctx context.Context) {
		for range 60 {
			if reportUpdate(ctx) {
				fleetAgent.Nudge()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
		}
	})
	sso := oidc.New(st, sessions, log)
	sso.Audit = auditLog
	// Addon web UIs and dev servers open only to users signed in to Envoryx who may use
	// them; the proxy asks again every few seconds whether that still holds.
	var gate *proxy.Gate
	if proxyInfo.Enabled {
		gate = proxy.NewGate(func(ctx context.Context, sessionID, projectID, need string) bool {
			p, err := sessions.ValidateSession(ctx, sessionID)
			if err != nil || p.TwoFactorSetup || manager.Suspension().Err() != nil {
				return false
			}
			return p.Require(auth.Scope(need), projectID) == nil
		})
		// Scripts, tests and monitoring send an API token instead.
		gate.SetTokenCheck(func(ctx context.Context, token, projectID, need string) bool {
			p, err := sessions.ValidateAPIToken(ctx, token)
			if err != nil || manager.Suspension().Err() != nil {
				return false
			}
			return p.Require(auth.Scope(need), projectID) == nil
		})
	}
	a := api.New(api.Deps{
		Gate: gate, Config: cfg, Version: version, Store: st, Auth: sessions, Audit: auditLog, Engine: engine,
		Projects: manager, Catalog: catalog, Stats: collector, HostPath: resolver, Certs: certs, ACME: acmeMgr, Notify: notifier, Proxy: proxyInfo,
		MCP: mcpSrv.Handler(), SSH: sshInfo, Log: log, StartedAt: time.Now(), Updates: updates, OIDC: sso,
		Instance: backups, DB: sqlDB, Restart: requestRestart, Warnings: warnings, Offsite: syncer, Fleet: fleetAgent,
	})
	// What the plan fixes applies now and whenever the plan changes.
	applyPlanSettings := func() {
		if err := a.ApplyPlanSettings(ctx); err != nil {
			log.Warn("plan: settings not applied", "err", err)
			msg := err.Error()
			planSettingsErr.Store(&msg)
			return
		}
		planSettingsErr.Store(nil)
	}
	applyPlanSettings()
	plans.OnChange(func(*plan.Plan) { applyPlanSettings() })
	// The plan's offsite target is the hoster's: it joins the admins' own targets.
	applyPlanOffsite := func(p *plan.Plan) {
		if syncer == nil {
			return
		}
		planOffsiteErr.Store(nil)
		if p == nil || p.Offsite == nil {
			syncer.Config.SetManaged(nil)
			return
		}
		t, err := offsite.FromPlan(p.Offsite, instanceID)
		if err != nil {
			log.Warn("plan: offsite target not used", "err", err)
			msg := err.Error()
			planOffsiteErr.Store(&msg)
			syncer.Config.SetManaged(nil)
			return
		}
		syncer.Config.SetManaged(&t)
	}
	applyPlanOffsite(plans.Get())
	plans.OnChange(applyPlanOffsite)
	background("plan", func(ctx context.Context) { plans.Run(ctx, 30*time.Second) })
	var origins []string
	if cfg.DevMode {
		origins = append(origins, cfg.DevOrigin)
		if u, err := url.Parse(cfg.DevOrigin); err == nil {
			a.SetAllowedOriginHosts([]string{u.Host})
		}
	}
	// On SIGTERM (Docker stop, Unraid update) or a requested restart, running project
	// operations get cfg.ShutdownGrace to finish before they are abandoned; new ones are
	// refused meanwhile. Whatever is cut off is recorded on the project as interrupted.
	// With "projects follow Envoryx" on, an external stop then takes the project
	// containers down as well (a restart Envoryx asked for itself does not: the process
	// is back in a moment and the projects would only be bounced).
	drain := func() {
		log.Info("draining project operations", "grace", cfg.ShutdownGrace)
		if !manager.Shutdown(cfg.ShutdownGrace) {
			log.Warn("project operations still running after the grace period were interrupted; affected projects carry the note in their status")
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if !restart.Load() && !updating.Load() && manager.ProjectsFollowEnvoryx(stopCtx) {
			begin := time.Now()
			log.Info("stopping project containers with Envoryx")
			manager.StopAllForShutdown(stopCtx)
			log.Info("project containers stopped", "took", time.Since(begin).Round(time.Millisecond))
		}
	}
	probeHost := func(ctx context.Context) string { return project.ProbeHostname(manager.BaseDomain(ctx)) }
	srv := server.New(server.Options{Addr: cfg.ListenAddr, AllowedOrigins: origins, Log: log, MCP: mcpSrv.Handler(), BeforeShutdown: drain, ProbeHost: probeHost}, a, sessions, dist)

	// 8. Embedded reverse proxy (host-name routing + HTTPS for projects and the UI).
	if proxyInfo.Enabled {
		source := func(ctx context.Context) (proxy.Table, error) {
			return manager.RouteTable(ctx, project.ProxyOptions{HTTPSPort: proxyInfo.HTTPSPort, EnvoryxURL: uiURL(ctx), ExtraUIHosts: []string{publicHost(ctx)}})
		}
		router := proxy.NewRouter(source, 2*time.Second, log)
		proxyInfo.Invalidate = router.Invalidate
		handler := proxy.NewHandler(router, srv.Handler(), certs != nil, log)
		handler.OnVisit = manager.Visited
		gate.SetRouter(router)
		handler.SetGate(gate)
		httpsAddr := ""
		if certs != nil {
			httpsAddr = cfg.ProxyHTTPS
		}
		ps := proxy.NewServer(handler, router, certs, cfg.ProxyHTTP, httpsAddr, nil, log)
		manager.SetShareProxy(cfg.ProxyHTTP)
		if certs != nil {
			// Certificates of host names nothing routes to any more go once at start
			// (projects deleted before this cleanup existed) and after every deletion.
			pruneCerts := func(ctx context.Context) {
				removed, err := ps.PruneCertificates(ctx)
				if err != nil {
					log.Warn("certificate cleanup skipped", "err", err)
					return
				}
				if len(removed) > 0 {
					log.Info("removed certificates of host names no longer in use", "hosts", removed)
				}
			}
			manager.SetDeletedHook(func(ctx context.Context, _ string) { pruneCerts(ctx) })
			background("certificate cleanup", pruneCerts)
		}
		background("proxy", func(ctx context.Context) {
			if err := ps.Run(ctx); err != nil {
				log.Warn("embedded proxy disabled", "err", err)
			}
		})
	}

	if notifier != nil {
		notifier.Notify(ctx, notify.Event{Kind: "envoryx.started", Level: notify.Info, Title: "Envoryx started", Message: "Version " + version + " is up."})
	}
	background("update check", updates.Run)
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

// publishInterface is the host address project ports are published on; "" for all.
func publishInterface(local bool) string {
	if local {
		return "127.0.0.1"
	}
	return ""
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

// newestBackupHint names the newest instance backup of a kind ("" for any kind) for
// error messages.
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

// fleetStatus is what the instance reports to its fleet manager.
func fleetStatus(ctx context.Context, st *store.Store, m *project.Manager, version, instanceID string) fleet.Status {
	s := fleet.Status{Version: version, InstanceID: instanceID}
	if projects, err := st.Projects.List(ctx); err == nil {
		s.Usage.Projects = len(projects)
		for _, p := range projects {
			if p.DesiredState == store.DesiredRunning {
				s.Usage.RunningProjects++
			}
			if p.Lifecycle == store.LifecycleFailed {
				s.Warnings = append(s.Warnings, fmt.Sprintf("project %s failed: %s", p.Name, p.LastError))
			}
		}
	}
	s.ImageUpdates = m.ProjectsWithNewerImages()
	s.Usage.Users, _ = st.Users.CountActive(ctx)
	s.Usage.DiskBytes, _ = m.DiskUsage()
	return s
}

// adoptRestoredInstanceID takes over the instance ID a restored instance backup was made
// under when its project containers are left on this Docker host and nothing else can
// claim them (instance.AdoptableID): the start after /config was lost and the backup
// restored. It returns the adopted ID, or "" when the instance keeps its own.
func adoptRestoredInstanceID(ctx context.Context, configDir, hint, current string, engine *docker.MobyEngine, st *store.Store, self string, log *slog.Logger) string {
	if hint == "" || hint == current {
		return ""
	}
	containers, err := engine.ListContainers(ctx, false, "")
	if err != nil {
		log.Warn("could not list the containers to check the restored instance ID", "err", err)
		return ""
	}
	list, err := st.Projects.List(ctx)
	if err != nil {
		log.Warn("could not list the projects to check the restored instance ID", "err", err)
		return ""
	}
	projects := map[string]bool{}
	for _, p := range list {
		projects[p.ID] = true
	}
	ok, reason := instance.AdoptableID(hint, current, containers, projects, self)
	if !ok {
		if reason != "" {
			log.Warn("the restored projects' containers on this Docker host carry the ID of the instance the backup was made on; this instance keeps its own ID, so they count as another instance's and the projects cannot start. If they are this Envoryx's own (its /config was lost), stop Envoryx, write that ID into the instance ID file and start it again",
				"backupInstanceId", hint, "instanceId", current, "file", filepath.Join(configDir, instance.IDFile), "reason", reason)
		}
		return ""
	}
	if err := instance.WriteID(configDir, hint); err != nil {
		log.Warn("could not adopt the restored instance ID", "id", hint, "err", err)
		return ""
	}
	engine.SetInstance(hint)
	log.Info("adopted the instance ID of the restored backup: its project containers are on this Docker host and no other Envoryx is", "id", hint, "previous", current)
	return hint
}
