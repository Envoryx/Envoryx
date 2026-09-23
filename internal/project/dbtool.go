package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// The database browser is one Adminer container shared by every project. It is started on
// the first use, joins the network of each project database it opens, and is reached
// through Envoryx's own UI at /dbtool/ so the Envoryx session protects it. Credentials
// never travel through the browser: Envoryx writes them to a file only the container can
// read, and a small Adminer plugin logs in with them.
const (
	// SettingDBToolEnabled is the settings key of the opt-in.
	SettingDBToolEnabled = "dbtool_enabled"
	// settingDBToolPort remembers the host port used on bare metal (no shared network).
	settingDBToolPort = "dbtool_host_port"

	DBToolImage     = "adminer:5"
	DBToolContainer = "envoryx-dbtool"
	DBToolNetwork   = "envoryx-dbtool"
	dbToolSystem    = "dbtool"
	dbToolPort      = 8080
	// DBToolPathPrefix is where the UI server mounts the reverse proxy.
	DBToolPathPrefix = "/dbtool"
)

// dbToolPlugin is the Adminer plugin mounted into the container. When the page is opened
// with the server/user chosen in Envoryx, it submits the login form with the password from
// the connections file instead of asking for it.
const dbToolPlugin = `<?php
// Envoryx: log in to the project database selected in Envoryx without asking for the
// password. Credentials are read from a file Envoryx maintains; anything not listed there
// gets Adminer's normal login form.
class AdminerEnvoryx extends Adminer\Plugin {
	private $connections;

	function __construct() {
		$this->connections = json_decode((string) @file_get_contents('/envoryx/connections.json'), true) ?: array();
	}

	private function known() {
		$key = Adminer\DRIVER . '|' . Adminer\SERVER . '|' . (string) ($_GET['username'] ?? '');
		return $this->connections[$key] ?? null;
	}

	function loginForm() {
		$c = $this->known();
		if (!$c) {
			return null;
		}
		foreach (array('driver' => Adminer\DRIVER, 'server' => Adminer\SERVER, 'username' => $c['username'], 'password' => $c['password'], 'db' => (string) ($_GET['db'] ?? $c['database'])) as $name => $value) {
			echo "<input type='hidden' name='auth[" . $name . "]' value='" . Adminer\h($value) . "'>\n";
		}
		echo "<p>Connecting to " . Adminer\h(Adminer\SERVER) . "…</p>\n";
		echo "<script" . Adminer\nonce() . ">document.forms[0].submit();</script>\n";
		return true;
	}

	function login($login, $password) {
		if ($this->known()) {
			return true;
		}
		return null;
	}
}

return new AdminerEnvoryx;
`

// dbToolConnection is one entry of the connections file, keyed "driver|server|username".
type dbToolConnection struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
	Project  string `json:"project"`
}

// DBToolStatus describes the database browser for the settings page.
type DBToolStatus struct {
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	ContainerID string `json:"containerId,omitempty"`
	Image       string `json:"image"`
	// Supported lists the database types Adminer can open (MongoDB is not among them).
	Supported []string `json:"supported"`
}

// DBToolLink is where a project's database opens in the browser.
type DBToolLink struct {
	URL      string `json:"url"`
	Server   string `json:"server"`
	Username string `json:"username"`
	Database string `json:"database"`
}

// ErrDBToolDisabled is returned when the browser is used without being enabled.
var ErrDBToolDisabled = errors.New("the database browser is disabled; enable it in Settings")

// dbToolDrivers maps Envoryx database variants to Adminer driver ids (which double as the
// URL parameter name). MongoDB is missing: the official Adminer image has no driver for it.
var dbToolDrivers = map[string]string{"mariadb": "server", "mysql": "server", "postgresql": "pgsql", "postgres": "pgsql"}

// DBToolEnabled reports the opt-in.
func (m *Manager) DBToolEnabled(ctx context.Context) bool {
	v, err := m.store.Settings.Get(ctx, SettingDBToolEnabled)
	return err == nil && v == "true"
}

// SetDBToolEnabled switches the browser on or off. Switching off removes the container,
// its network and the credentials file.
func (m *Manager) SetDBToolEnabled(ctx context.Context, on bool) error {
	unlock, err := m.lock(dbToolSystem)
	if err != nil {
		return err
	}
	defer unlock()
	if err := m.store.Settings.Set(ctx, SettingDBToolEnabled, strconv.FormatBool(on)); err != nil {
		return err
	}
	if !on {
		if err := m.removeDBTool(ctx); err != nil {
			return err
		}
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"dbToolEnabled": on})
	return nil
}

// DBToolStatus returns enabled/running state.
func (m *Manager) DBToolStatus(ctx context.Context) (DBToolStatus, error) {
	st := DBToolStatus{Enabled: m.DBToolEnabled(ctx), Image: DBToolImage, Supported: []string{"mariadb", "mysql", "postgresql"}}
	c, err := m.findDBTool(ctx)
	if err != nil {
		return st, err
	}
	if c != nil {
		st.ContainerID = c.ID
		st.Running = c.State == "running"
	}
	return st, nil
}

// OpenDBTool prepares the browser for a project's database: starts the container when
// needed, refreshes the credentials file, joins the project network and returns the URL
// (relative to the Envoryx UI) that logs straight in.
func (m *Manager) OpenDBTool(ctx context.Context, id string) (DBToolLink, error) {
	if err := validate.UUID(id); err != nil {
		return DBToolLink{}, ErrNotFound
	}
	if !m.DBToolEnabled(ctx) {
		return DBToolLink{}, ErrDBToolDisabled
	}
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return DBToolLink{}, err
	}
	svc, cfg, err := databaseConfig(proj)
	if err != nil {
		return DBToolLink{}, err
	}
	driver, ok := dbToolDrivers[svc.Variant]
	if !ok {
		return DBToolLink{}, fmt.Errorf("%w: the database browser cannot open %s databases; use the published port with a desktop client", validate.ErrInvalid, svc.Variant)
	}

	unlock, err := m.lock(dbToolSystem)
	if err != nil {
		return DBToolLink{}, err
	}
	defer unlock()
	if err := m.ensureDBTool(ctx); err != nil {
		return DBToolLink{}, err
	}
	if err := m.writeDBToolConnections(ctx); err != nil {
		return DBToolLink{}, err
	}
	c, err := m.findDBTool(ctx)
	if err != nil {
		return DBToolLink{}, err
	}
	if c == nil {
		return DBToolLink{}, errors.New("database browser container disappeared")
	}
	if err := m.connectDBTool(ctx, c.ID, NetworkName(proj.Slug)); err != nil {
		return DBToolLink{}, err
	}
	server := ContainerName(proj.Slug, store.ServiceDatabase)
	q := url.Values{}
	q.Set(driver, server)
	q.Set("username", cfg.Username)
	q.Set("db", cfg.Database)
	m.audit.Log(ctx, audit.ActionDBToolOpened, "project", id, map[string]any{"name": proj.Name})
	return DBToolLink{URL: DBToolPathPrefix + "/?" + q.Encode(), Server: server, Username: cfg.Username, Database: cfg.Database}, nil
}

// DBToolDial returns the upstream address of the browser for the reverse proxy: the
// container name on the shared network inside Docker, the published loopback port on bare
// metal. "" when the container is not running.
func (m *Manager) DBToolDial(ctx context.Context) (string, error) {
	c, err := m.findDBTool(ctx)
	if err != nil {
		return "", err
	}
	if c == nil || c.State != "running" {
		return "", nil
	}
	paths, err := m.paths()
	if err != nil {
		return "", err
	}
	if paths.SelfContainerID != "" {
		return net.JoinHostPort(DBToolContainer, strconv.Itoa(dbToolPort)), nil
	}
	for _, p := range c.Ports {
		if p.ContainerPort == dbToolPort && p.HostPort != 0 {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.HostPort)), nil
		}
	}
	return "", nil
}

func (m *Manager) findDBTool(ctx context.Context) (*docker.Container, error) {
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		return nil, err
	}
	for i := range containers {
		if containers[i].Labels[docker.LabelSystem] == dbToolSystem {
			return &containers[i], nil
		}
	}
	return nil, nil
}

func (m *Manager) dbToolDir(paths Paths) (dir, hostDir string) {
	return filepath.Join(paths.ConfigDir, "dbtool"), filepath.Join(paths.ConfigHostDir, "dbtool")
}

// ensureDBTool creates the network and container when missing and starts the container.
func (m *Manager) ensureDBTool(ctx context.Context) error {
	paths, err := m.paths()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	dir, hostDir := m.dbToolDir(paths)
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins", "envoryx.php"), []byte(dbToolPlugin), 0o640); err != nil {
		return err
	}
	// The container runs as the project user (PUID:PGID) like every other container
	// Envoryx starts, so the files are readable by exactly that user and root.
	for _, p := range []string{dir, filepath.Join(dir, "plugins"), filepath.Join(dir, "plugins", "envoryx.php")} {
		_ = os.Chown(p, paths.PUID, paths.PGID)
	}
	if _, err := os.Stat(filepath.Join(dir, "connections.json")); err != nil {
		if err := m.writeDBToolConnections(ctx); err != nil {
			return err
		}
	}
	labels := map[string]string{docker.LabelManaged: "true", docker.LabelSystem: dbToolSystem, docker.LabelService: dbToolSystem, docker.LabelVersion: paths.EnvoryxVersion}
	folder := m.FolderViewFolder(ctx)

	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	haveNet := false
	for _, n := range networks {
		if n.Name == DBToolNetwork {
			haveNet = true
		}
	}
	if !haveNet {
		if _, err := m.engine.CreateNetwork(ctx, DBToolNetwork, labels); err != nil {
			return fmt.Errorf("create network: %w", err)
		}
	}
	if err := m.attachProxy(ctx, DBToolNetwork); err != nil {
		m.log.Warn("proxy attach failed", "network", DBToolNetwork, "err", err)
	}

	c, err := m.findDBTool(ctx)
	if err != nil {
		return err
	}
	if c != nil && (c.Image != DBToolImage || c.Labels[docker.LabelFolderView] != folder) {
		// A newer Envoryx may ship another Adminer version, or the FolderView3 folder
		// changed (labels are fixed at creation): recreate, the container holds no state.
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
		c = nil
	}
	if c == nil {
		if err := m.engine.EnsureImage(ctx, DBToolImage, m.pullProgress(ctx, dbToolSystem, DBToolImage)); err != nil {
			return fmt.Errorf("pull image %s: %w", DBToolImage, err)
		}
		containerLabels := maps.Clone(labels)
		docker.AddUnraidLabels(containerLabels, folder)
		spec := docker.ContainerSpec{
			Name: DBToolContainer, Image: DBToolImage, Labels: containerLabels,
			// Directories, not single files: the connections file is replaced by rename and
			// a file bind mount would keep showing the old inode.
			Mounts: []docker.MountSpec{
				{Type: "bind", Source: filepath.Join(hostDir, "plugins"), Target: "/var/www/html/plugins-enabled", ReadOnly: true},
				{Type: "bind", Source: hostDir, Target: "/envoryx", ReadOnly: true},
			},
			Network: DBToolNetwork, NetworkAlias: []string{DBToolContainer}, RestartPolicy: "unless-stopped", StopTimeout: 5,
			User: fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
		}
		if paths.SelfContainerID == "" {
			// Bare metal: no shared network, so publish on the loopback interface.
			port, err := m.dbToolHostPort(ctx)
			if err != nil {
				return err
			}
			spec.Ports = []docker.PortSpec{{HostIP: "127.0.0.1", HostPort: port, ContainerPort: dbToolPort, Protocol: "tcp"}}
		}
		id, err := m.engine.CreateContainer(ctx, spec)
		if err != nil {
			return fmt.Errorf("create container: %w", err)
		}
		c = &docker.Container{ID: id, State: "created"}
		m.log.Info("database browser created", "image", DBToolImage)
	}
	if c.State != "running" {
		if err := m.engine.StartContainer(ctx, c.ID); err != nil {
			return fmt.Errorf("start database browser: %w", err)
		}
	}
	return nil
}

// dbToolHostPort allocates (once) the loopback port used on bare metal.
func (m *Manager) dbToolHostPort(ctx context.Context) (int, error) {
	if v, err := m.store.Settings.Get(ctx, settingDBToolPort); err == nil {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p, nil
		}
	}
	port, err := m.allocatePort(ctx)
	if err != nil {
		return 0, err
	}
	return port, m.store.Settings.Set(ctx, settingDBToolPort, strconv.Itoa(port))
}

// connectDBTool joins the browser to a project network (idempotent).
func (m *Manager) connectDBTool(ctx context.Context, containerID, network string) error {
	nets, err := m.engine.ContainerNetworks(ctx, containerID)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n == network {
			return nil
		}
	}
	if err := m.engine.ConnectNetwork(ctx, network, containerID); err != nil {
		return fmt.Errorf("attach database browser to %s: %w", network, err)
	}
	return nil
}

// detachDBTool removes the browser from a project network before the network goes away.
func (m *Manager) detachDBTool(ctx context.Context, network string) error {
	c, err := m.findDBTool(ctx)
	if err != nil || c == nil {
		return err
	}
	if err := m.engine.DisconnectNetwork(ctx, network, c.ID); err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "is not connected") {
		return err
	}
	return nil
}

// removeDBTool deletes the container, the network and the credentials file.
func (m *Manager) removeDBTool(ctx context.Context) error {
	c, err := m.findDBTool(ctx)
	if err != nil {
		return err
	}
	if c != nil {
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.Name == DBToolNetwork {
			_ = m.detachProxy(ctx, n.Name)
			if err := m.engine.RemoveNetwork(ctx, n.ID); err != nil {
				return err
			}
		}
	}
	if paths, err := m.paths(); err == nil {
		dir, _ := m.dbToolDir(paths)
		_ = os.Remove(filepath.Join(dir, "connections.json"))
	}
	m.log.Info("database browser removed")
	return nil
}

// writeDBToolConnections regenerates the credentials file from every project database
// Adminer can open. The file is replaced atomically so the container never reads a
// half-written one.
func (m *Manager) writeDBToolConnections(ctx context.Context) error {
	paths, err := m.paths()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return err
	}
	out := map[string]dbToolConnection{}
	for _, p := range projects {
		svc, cfg, err := databaseConfig(p)
		if err != nil {
			continue
		}
		driver, ok := dbToolDrivers[svc.Variant]
		if !ok {
			continue
		}
		server := ContainerName(p.Slug, store.ServiceDatabase)
		out[driver+"|"+server+"|"+cfg.Username] = dbToolConnection{Username: cfg.Username, Password: cfg.Password, Database: cfg.Database, Project: p.Name}
		if cfg.RootPassword != "" {
			root := "root"
			if driver == "pgsql" {
				root = "postgres"
			}
			out[driver+"|"+server+"|"+root] = dbToolConnection{Username: root, Password: cfg.RootPassword, Database: cfg.Database, Project: p.Name}
		}
	}
	dir, _ := m.dbToolDir(paths)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	_ = os.Chown(dir, paths.PUID, paths.PGID)
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".connections.json.tmp")
	// Readable by root and the container's user (PUID); the directory lives in /config,
	// which project containers cannot reach.
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	_ = os.Chown(tmp, paths.PUID, paths.PGID)
	return os.Rename(tmp, filepath.Join(dir, "connections.json"))
}
