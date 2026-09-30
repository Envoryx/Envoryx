package project

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// The database browser is one Adminer container shared by every project. It is started on
// the first use, joins the network of each project database it opens, and is reached
// through Envoryx's own UI at /dbtool/, so only an Envoryx session with operate access
// gets to it. Envoryx writes the credentials to a file only the container can read, and a
// small Adminer plugin logs in with them. The password does reach the browser: the
// plugin puts it into the hidden login form it submits right away, so whoever may open
// the page can read it from there.
//
// The container is shared, so it must not decide on its own what a request may open. The
// proxy checks every request's target against the principal's projects (see
// DBToolProjects) and tells the container which login it checked; the container answers
// only requests that carry the proxy's secret, because project containers on the networks
// it joined could reach it directly.
const (
	// SettingDBToolEnabled is the settings key of the opt-in.
	SettingDBToolEnabled = "dbtool_enabled"
	// settingDBToolPort remembers the host port used on bare metal (no shared network).
	settingDBToolPort = "dbtool_host_port"

	DBToolImage = "adminer:5"
	// legacyDBToolName is the container and network name from before instance IDs. It
	// allowed one database browser per Docker host; see DBToolName.
	legacyDBToolName = "envoryx-dbtool"
	dbToolSystem     = "dbtool"
	dbToolPort       = 8080
	// DBToolPathPrefix is where the UI server mounts the reverse proxy.
	DBToolPathPrefix = "/dbtool"
	// DBToolTokenHeader carries the proxy's secret; the container refuses requests without it.
	DBToolTokenHeader = "X-Envoryx-Dbtool-Token"
	// DBToolTargetHeader names the login ("driver|server|username") the proxy checked for a
	// request; the plugin refuses to connect anywhere else.
	DBToolTargetHeader = "X-Envoryx-Dbtool-Target"
	dbToolTokenFile    = "proxy-token"
)

// dbToolRouter is the script PHP's built-in server runs before every request. Without the
// proxy's secret nothing is served, and only Adminer's entry page is reachable at all: a
// request for adminer.php would run Adminer without the plugins.
const dbToolRouter = `<?php
// Envoryx: only the Envoryx proxy may use this container.
$token = trim((string) @file_get_contents('/envoryx/proxy-token'));
if ($token === '' || !hash_equals($token, (string) ($_SERVER['HTTP_X_ENVORYX_DBTOOL_TOKEN'] ?? ''))) {
	http_response_code(403);
	echo "Open the database browser from Envoryx.\n";
	return true;
}
$path = (string) parse_url((string) $_SERVER['REQUEST_URI'], PHP_URL_PATH);
if ($path !== '/' && $path !== '/index.php') {
	http_response_code(404);
	return true;
}
return false;
`

// dbToolPlugin is the Adminer plugin mounted into the container. When the page is opened
// with the server/user chosen in Envoryx, it renders the login form with the password from
// the connections file in a hidden field and submits it instead of asking.
const dbToolPlugin = `<?php
// Envoryx: log in to the project database selected in Envoryx without asking for the
// password. Credentials are read from a file Envoryx maintains; anything not listed there
// gets Adminer's normal login form. Adminer connects only to the login the Envoryx proxy
// checked for this request, so the two can never disagree about what a request opens.
class AdminerEnvoryx extends Adminer\Plugin {
	private $connections;

	function __construct() {
		$this->connections = json_decode((string) @file_get_contents('/envoryx/connections.json'), true) ?: array();
	}

	private function target() {
		return Adminer\DRIVER . '|' . Adminer\SERVER . '|' . (string) ($_GET['username'] ?? '');
	}

	private function checked() {
		return (string) ($_SERVER['HTTP_X_ENVORYX_DBTOOL_TARGET'] ?? '') === $this->target();
	}

	private function known() {
		return $this->checked() ? ($this->connections[$this->target()] ?? null) : null;
	}

	function credentials() {
		if (!$this->checked()) {
			header('HTTP/1.1 403 Forbidden');
			echo "Open the database from Envoryx.\n";
			exit;
		}
		return null;
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
		if (!$this->checked()) {
			return 'Open the database from Envoryx.';
		}
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
		// One under the old host-wide name is replaced by the next open (see ensureDBTool).
		st.Running = c.State == "running" && c.Name == m.dbToolName()
	}
	return st, nil
}

// dbToolHostsLabel marks a browser container created with the host gateway entry.
const dbToolHostsLabel = "envoryx.dbtool.hostgateway"

// dbToolGuardLabel marks a browser container that runs behind the router script and so
// answers only the proxy. An older container would serve anyone on its networks.
const dbToolGuardLabel = "envoryx.dbtool.guard"

// generatedInstanceID matches the IDs instance.LoadID creates.
var generatedInstanceID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// DBToolName is the name of the browser's container and of its network for an instance.
// Docker names are host-wide, so each instance on a shared host gets its own: a generated
// ID contributes its first 8 characters, a hand-written one (which may share a prefix with
// another) all of it, shortened with a hash where the name would outgrow a DNS label (the
// proxy dials the container by name).
func DBToolName(instance string) string {
	switch {
	case instance == "":
		return legacyDBToolName
	case generatedInstanceID.MatchString(instance):
		instance = instance[:8]
	case len(instance) > 48:
		sum := sha256.Sum256([]byte(instance))
		instance = strings.TrimRight(instance[:39], "-") + "-" + hex.EncodeToString(sum[:])[:8]
	}
	return legacyDBToolName + "-" + instance
}

func (m *Manager) dbToolName() string { return DBToolName(m.engine.InstanceID()) }

// dbToolServer is the server Adminer connects to: the database container by name, or an
// external server's address.
func dbToolServer(p store.Project, svc *store.ProjectService, cfg runtime.DatabaseConfig) string {
	if cfg.External() {
		return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	}
	return ContainerName(p.Slug, svc.Kind)
}

// OpenDBTool prepares the browser for a database of a project (db "" for the primary):
// starts the container when needed, refreshes the credentials file, joins the project
// network and returns the URL (relative to the Envoryx UI) that logs straight in.
func (m *Manager) OpenDBTool(ctx context.Context, id, db string) (DBToolLink, error) {
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
	svc, cfg, err := databaseOf(proj, db)
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
	server := dbToolServer(proj, svc, cfg)
	if !cfg.External() {
		// An external server is reached over the network Adminer already has.
		if err := m.connectDBTool(ctx, c.ID, NetworkName(proj.Slug)); err != nil {
			return DBToolLink{}, err
		}
	}
	q := url.Values{}
	q.Set(driver, server)
	q.Set("username", cfg.Username)
	q.Set("db", cfg.Database)
	m.audit.Log(ctx, audit.ActionDBToolOpened, "project", id, auditDB(map[string]any{"name": proj.Name}, db))
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
	if c == nil || c.State != "running" || c.Labels[dbToolGuardLabel] != "1" || c.Name != m.dbToolName() {
		// A container from before the router or under the host-wide name is not used;
		// opening a database recreates it.
		return "", nil
	}
	paths, err := m.paths()
	if err != nil {
		return "", err
	}
	if paths.SelfContainerID != "" {
		return net.JoinHostPort(c.Name, strconv.Itoa(dbToolPort)), nil
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
	if err := os.WriteFile(filepath.Join(dir, "router.php"), []byte(dbToolRouter), 0o640); err != nil {
		return err
	}
	if err := ensureDBToolToken(dir); err != nil {
		return err
	}
	// The container runs as the project user (PUID:PGID) like every other container
	// Envoryx starts, so the files are readable by exactly that user and root.
	for _, p := range []string{dir, filepath.Join(dir, "plugins"), filepath.Join(dir, "plugins", "envoryx.php"), filepath.Join(dir, "router.php"), filepath.Join(dir, dbToolTokenFile)} {
		_ = os.Chown(p, paths.PUID, paths.PGID)
	}
	if _, err := os.Stat(filepath.Join(dir, "connections.json")); err != nil {
		if err := m.writeDBToolConnections(ctx); err != nil {
			return err
		}
	}
	labels := map[string]string{docker.LabelManaged: "true", docker.LabelSystem: dbToolSystem, docker.LabelService: dbToolSystem, docker.LabelVersion: paths.EnvoryxVersion}
	folder := m.FolderViewFolder(ctx)
	name := m.dbToolName()

	c, err := m.findDBTool(ctx)
	if err != nil {
		return err
	}
	if c != nil && (c.Name != name || c.Image != DBToolImage || c.Labels[docker.LabelFolderView] != folder || c.Labels[dbToolHostsLabel] != "1" || c.Labels[dbToolGuardLabel] != "1") {
		// A newer Envoryx may ship another Adminer version, the FolderView3 folder
		// changed (labels are fixed at creation), or the container predates the host
		// gateway entry, the router or the per-instance name: recreate, the container
		// holds no state.
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
		c = nil
	}
	if err := m.removeLegacyDBToolNetwork(ctx); err != nil {
		m.log.Warn("remove the old database browser network", "err", err)
	}

	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	haveNet := false
	for _, n := range networks {
		if n.Name == name {
			haveNet = true
		}
	}
	if !haveNet {
		if err := m.createNetwork(ctx, name, labels); err != nil {
			return fmt.Errorf("create network: %w", err)
		}
	}
	if err := m.attachProxy(ctx, name, nil, false); err != nil {
		m.log.Warn("proxy attach failed", "network", name, "err", err)
	}
	if c == nil {
		if err := m.engine.EnsureImage(ctx, DBToolImage, m.pullProgress(ctx, dbToolSystem, DBToolImage)); err != nil {
			return fmt.Errorf("pull image %s: %w", DBToolImage, err)
		}
		containerLabels := maps.Clone(labels)
		containerLabels[dbToolHostsLabel] = "1"
		containerLabels[dbToolGuardLabel] = "1"
		docker.AddUnraidLabels(containerLabels, folder)
		spec := docker.ContainerSpec{
			Name: name, Image: DBToolImage, Labels: containerLabels,
			// Directories, not single files: the connections file is replaced by rename and
			// a file bind mount would keep showing the old inode.
			Mounts: []docker.MountSpec{
				{Type: "bind", Source: filepath.Join(hostDir, "plugins"), Target: "/var/www/html/plugins-enabled", ReadOnly: true},
				{Type: "bind", Source: hostDir, Target: "/envoryx", ReadOnly: true},
			},
			// The image's own command plus the router script, which turns away everything
			// that does not come through the proxy.
			Cmd:     []string{"php", "-S", "[::]:" + strconv.Itoa(dbToolPort), "-t", "/var/www/html", "/envoryx/router.php"},
			Network: name, NetworkAlias: []string{name}, RestartPolicy: "unless-stopped", StopTimeout: 5,
			User: fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
			// An external database may run on the Docker host itself.
			ExtraHosts: []string{hostGatewayEntry},
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

// ensureDBToolToken creates the proxy's secret once. It stays for the container's
// lifetime and goes with the credentials file when the browser is switched off.
func ensureDBToolToken(dir string) error {
	path := filepath.Join(dir, dbToolTokenFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hex.EncodeToString(b)), 0o640)
}

// DBToolProxyToken returns the secret the proxy presents to the browser container.
func (m *Manager) DBToolProxyToken() (string, error) {
	paths, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	dir, _ := m.dbToolDir(paths)
	raw, err := os.ReadFile(filepath.Join(dir, dbToolTokenFile))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("database browser token is empty")
	}
	return token, nil
}

// retireOutdatedDBTool removes a browser container from before the router, which answers
// anyone on the project networks it joined and logs them in, and one under the host-wide
// name of older versions, which keeps other instances on the host from having their own.
// The next open recreates it.
func (m *Manager) retireOutdatedDBTool(ctx context.Context, containers []docker.Container) {
	name := m.dbToolName()
	for _, c := range containers {
		if c.Labels[docker.LabelSystem] != dbToolSystem || (c.Labels[dbToolGuardLabel] == "1" && c.Name == name) {
			continue
		}
		unlock, err := m.lock(dbToolSystem)
		if err != nil {
			return // an open is running and recreates it anyway
		}
		if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
			m.log.Warn("remove outdated database browser", "err", err)
		} else {
			m.log.Info("outdated database browser removed; opening a database recreates it")
			if err := m.removeLegacyDBToolNetwork(ctx); err != nil {
				m.log.Warn("remove the old database browser network", "err", err)
			}
		}
		unlock()
		return
	}
}

// removeLegacyDBToolNetwork removes this instance's network under the host-wide name of
// older versions once the browser has moved to its own. Another instance's network of that
// name is not listed as managed and stays.
func (m *Manager) removeLegacyDBToolNetwork(ctx context.Context) error {
	if m.dbToolName() == legacyDBToolName {
		return nil
	}
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.Name == legacyDBToolName && n.Labels[docker.LabelSystem] == dbToolSystem {
			_ = m.detachProxy(ctx, n.Name)
			if err := m.engine.RemoveNetwork(ctx, n.ID); err != nil {
				return err
			}
		}
	}
	return nil
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
		if n.Name == m.dbToolName() || (n.Name == legacyDBToolName && n.Labels[docker.LabelSystem] == dbToolSystem) {
			_ = m.detachProxy(ctx, n.Name)
			if err := m.engine.RemoveNetwork(ctx, n.ID); err != nil {
				return err
			}
		}
	}
	if paths, err := m.paths(); err == nil {
		dir, _ := m.dbToolDir(paths)
		_ = os.Remove(filepath.Join(dir, "connections.json"))
		_ = os.Remove(filepath.Join(dir, dbToolTokenFile))
	}
	m.log.Info("database browser removed")
	return nil
}

// dbToolLogin is a login the browser can open and the project it belongs to.
type dbToolLogin struct {
	target    DBToolTarget
	conn      dbToolConnection
	projectID string
}

// DBToolTarget is what an Adminer request opens: the driver id (Adminer's URL parameter,
// "server" for MySQL/MariaDB), the server and the user.
type DBToolTarget struct {
	Driver   string
	Server   string
	Username string
}

// Key is the target as the connections file and the plugin spell it.
func (t DBToolTarget) Key() string { return t.Driver + "|" + t.Server + "|" + t.Username }

// dbToolLogins lists every login of every project database Adminer can open: the
// application user and, where the database has one, the administrator.
func (m *Manager) dbToolLogins(ctx context.Context) ([]dbToolLogin, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []dbToolLogin
	for _, p := range projects {
		for _, dbSvc := range p.Databases() {
			svc, cfg, err := databaseOf(p, dbSvc.Kind.DatabaseName())
			if err != nil {
				continue
			}
			driver, ok := dbToolDrivers[svc.Variant]
			if !ok {
				continue
			}
			server := dbToolServer(p, svc, cfg)
			out = append(out, dbToolLogin{DBToolTarget{driver, server, cfg.Username}, dbToolConnection{Username: cfg.Username, Password: cfg.Password, Database: cfg.Database, Project: p.Name}, p.ID})
			if cfg.RootPassword != "" {
				root := "root"
				if driver == "pgsql" {
					root = "postgres"
				}
				out = append(out, dbToolLogin{DBToolTarget{driver, server, root}, dbToolConnection{Username: root, Password: cfg.RootPassword, Database: cfg.Database, Project: p.Name}, p.ID})
			}
		}
	}
	return out, nil
}

// DBToolProjects returns the projects a browser target belongs to: those with exactly this
// login, or, for a target without a user (Adminer's login page of a server), those with
// any login on the server. None means the browser has no business with the target. The
// administrator login counts like the application user: both passwords are shown to
// whoever may operate the project (DatabaseCredentials).
func (m *Manager) DBToolProjects(ctx context.Context, t DBToolTarget) ([]string, error) {
	logins, err := m.dbToolLogins(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range logins {
		match := l.target == t || (t.Username == "" && l.target.Driver == t.Driver && l.target.Server == t.Server)
		if match && !slices.Contains(ids, l.projectID) {
			ids = append(ids, l.projectID)
		}
	}
	return ids, nil
}

// writeDBToolConnections regenerates the credentials file from every project database
// Adminer can open. The file is replaced atomically so the container never reads a
// half-written one.
func (m *Manager) writeDBToolConnections(ctx context.Context) error {
	paths, err := m.paths()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	logins, err := m.dbToolLogins(ctx)
	if err != nil {
		return err
	}
	out := map[string]dbToolConnection{}
	for _, l := range logins {
		out[l.target.Key()] = l.conn
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
