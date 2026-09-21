package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/disk"
	"github.com/envoryx/envoryx/internal/project"
)

// Diagnostics answer "is everything set up right?" in one place: every check names what
// it looked at, what it found and – when something is off – what to do about it. Titles
// and hints are stable English strings the UI translates; details carry live values.
//
// Statuses: ok, info (nothing to fix, worth knowing), warning (something will not work
// as expected), error (something is broken).
type Check struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	// Action is a one-click fix the UI can offer.
	Action *CheckAction `json:"action,omitempty"`
	// Docs is the DEPLOYMENT.md anchor with the long explanation.
	Docs string `json:"docs,omitempty"`
}

// CheckAction is a fix the UI performs on request.
type CheckAction struct {
	// Kind: "setPublicHost" (Value = host), "settingsTab" (Value = tab name) or "link"
	// (Value = path inside the UI).
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Label is the button text; empty for setPublicHost, which the UI labels itself.
	Label string `json:"label,omitempty"`
}

const (
	checkOK      = "ok"
	checkInfo    = "info"
	checkWarning = "warning"
	checkError   = "error"

	catRuntime     = "runtime"
	catNetwork     = "network"
	catSecurity    = "security"
	catMaintenance = "maintenance"
)

// diagnostics runs every check with a short deadline and returns them grouped by
// category with a summary.
func (a *API) diagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	checks := a.runChecks(ctx)
	summary := map[string]int{checkOK: 0, checkInfo: 0, checkWarning: 0, checkError: 0}
	for _, c := range checks {
		summary[c.Status]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks, "summary": summary, "at": time.Now().UTC()})
}

// runChecks executes the independent checks concurrently; each one is responsible for
// its own timeout so one slow probe (DNS) does not hold the page.
func (a *API) runChecks(ctx context.Context) []Check {
	funcs := []func(context.Context) []Check{
		a.checkDocker, a.checkConfigStorage, a.checkHostPaths, a.checkDiskSpace, a.checkBackupsDir, a.checkDatabase,
		a.checkPublicHost, a.checkProxyPorts, a.checkDNS, a.checkSSH,
		a.checkTLS,
		a.checkUpdate, a.checkReconcile, a.checkNotifications,
	}
	results := make([][]Check, len(funcs))
	var wg sync.WaitGroup
	for i, f := range funcs {
		wg.Add(1)
		go func(i int, f func(context.Context) []Check) {
			defer wg.Done()
			results[i] = f(ctx)
		}(i, f)
	}
	wg.Wait()
	var out []Check
	for _, r := range results {
		out = append(out, r...)
	}
	order := map[string]int{catRuntime: 0, catNetwork: 1, catSecurity: 2, catMaintenance: 3}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Category] < order[out[j].Category] })
	return out
}

// ---- runtime -----------------------------------------------------------------------------

func (a *API) checkDocker(ctx context.Context) []Check {
	c := Check{ID: "docker.engine", Category: catRuntime, Title: "Docker engine", Docs: "requirements"}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := a.d.Engine.Ping(ctx)
	if err != nil {
		c.Status, c.Detail = checkError, err.Error()
		c.Hint = "Envoryx cannot manage containers without the engine. Check that /var/run/docker.sock is mounted into the container (or DOCKER_HOST points at a reachable daemon) and that the daemon is running."
		return []Check{c}
	}
	c.Status = checkOK
	c.Detail = fmt.Sprintf("Docker %s (API %s) on %s, %d containers running", info.ServerVersion, info.APIVersion, info.OS, info.Running)
	if info.Hostname != "" {
		c.Detail += " – host " + info.Hostname
	}
	return []Check{c}
}

func (a *API) checkConfigStorage(context.Context) []Check {
	c := Check{ID: "storage.config", Category: catRuntime, Title: "Configuration storage", Docs: "where-to-put-config"}
	if len(a.d.Warnings) > 0 {
		c.Status, c.Detail = checkWarning, strings.Join(a.d.Warnings, " ")
		c.Hint = "Keep /config on local storage (on Unraid: the cache pool / appdata). SQLite on network or FUSE storage corrupts sooner or later."
		return []Check{c}
	}
	kind := config.CheckStorage(a.d.Config.ConfigDir)
	c.Status = checkOK
	c.Detail = a.d.Config.ConfigDir
	if kind.Name != "" {
		c.Detail += " (" + kind.Name + ")"
	}
	return []Check{c}
}

func (a *API) checkHostPaths(context.Context) []Check {
	c := Check{ID: "storage.hostpaths", Category: catRuntime, Title: "Host paths", Docs: "host-paths-important"}
	st := a.d.HostPath.Status()
	if st.BareMetal {
		c.Status, c.Detail = checkOK, "Envoryx runs directly on the host; container paths are host paths."
		return []Check{c}
	}
	resolve := func(dir string) string {
		if v, ok := st.Overrides[dir]; ok {
			return v
		}
		return st.Detected[dir]
	}
	projects, cfgDir := resolve(a.d.Config.ProjectsDir), resolve(a.d.Config.ConfigDir)
	switch {
	case projects == "" || cfgDir == "":
		c.Status = checkError
		c.Detail = "Could not determine where /projects or /config live on the host."
		if st.Error != "" {
			c.Detail += " " + st.Error
		}
		c.Hint = "Project containers mount these directories from the host, so Envoryx must know the host paths. Set ENVORYX_PROJECTS_HOST_PATH and ENVORYX_CONFIG_HOST_PATH to the paths you mapped (e.g. /mnt/user/development, /mnt/user/appdata/envoryx)."
	default:
		c.Status = checkOK
		c.Detail = fmt.Sprintf("%s → %s, %s → %s", a.d.Config.ProjectsDir, projects, a.d.Config.ConfigDir, cfgDir)
	}
	return []Check{c}
}

func (a *API) checkDiskSpace(context.Context) []Check {
	c := Check{ID: "storage.disk", Category: catRuntime, Title: "Disk space"}
	dirs := []string{a.d.Config.ConfigDir, a.d.Config.ProjectsDir}
	if a.d.Config.BackupsDir != "" {
		dirs = append(dirs, a.d.Config.BackupsDir)
	}
	usages := disk.Check(dirs...)
	if len(usages) == 0 {
		c.Status, c.Detail = checkInfo, "Free space could not be determined."
		return []Check{c}
	}
	var parts, low []string
	for _, u := range usages {
		parts = append(parts, fmt.Sprintf("%s: %s free of %s", u.Path, disk.Human(u.FreeBytes), disk.Human(u.TotalBytes)))
		if u.Low {
			low = append(low, u.Path)
		}
	}
	c.Detail = strings.Join(parts, " · ")
	if len(low) > 0 {
		c.Status = checkWarning
		c.Hint = "Below 2 GiB or 5 % free, backups and image pulls start failing. Free space or move the directory to a larger disk."
	} else {
		c.Status = checkOK
	}
	return []Check{c}
}

func (a *API) checkBackupsDir(context.Context) []Check {
	c := Check{ID: "storage.backups", Category: catRuntime, Title: "Backup directory", Docs: "backups"}
	dir := a.d.Config.BackupsDir
	if dir == "" {
		dir = filepath.Join(a.d.Config.ConfigDir, "backups")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.Status, c.Detail = checkError, err.Error()
		c.Hint = "Backups cannot be written. Check the mount and its permissions for the container user (PUID/PGID)."
		return []Check{c}
	}
	probe, err := os.CreateTemp(dir, ".envoryx-diagnostics-*")
	if err != nil {
		c.Status, c.Detail = checkError, err.Error()
		c.Hint = "The directory exists but is not writable – a read-only mount or wrong permissions. Backups will fail until this is fixed."
		return []Check{c}
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	c.Status, c.Detail = checkOK, dir
	if kind := config.CheckStorage(dir); kind.Name != "" {
		c.Detail += " (" + kind.Name + ")"
	}
	return []Check{c}
}

func (a *API) checkDatabase(ctx context.Context) []Check {
	c := Check{ID: "storage.database", Category: catRuntime, Title: "Database"}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var result string
	if err := a.d.Store.DB().QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		c.Status, c.Detail = checkError, err.Error()
		c.Hint = "The database could not be checked. Restore the newest instance backup if this persists."
		return []Check{c}
	}
	if result != "ok" {
		c.Status, c.Detail = checkError, result
		c.Hint = "SQLite reports damage. Stop making changes, take an instance backup for forensics and restore the newest good one (Settings → Backups)."
		return []Check{c}
	}
	c.Status, c.Detail = checkOK, "SQLite integrity check passed"
	return []Check{c}
}

// ---- network -----------------------------------------------------------------------------

func (a *API) checkPublicHost(ctx context.Context) []Check {
	c := Check{ID: "network.publicHost", Category: catNetwork, Title: "Host for project links", Docs: "project-links-and-the-envoryx-containers-own-ip"}
	needed, suggestion := a.publicHostAdvice(ctx)
	host := a.publicHost(ctx)
	switch {
	case needed:
		c.Status = checkWarning
		c.Detail = fmt.Sprintf("Envoryx has its own IP (%s) but no host for project links is set; links to published ports (project URLs, Mailpit, object storage console, database ports) point at Envoryx instead of the Docker host.", a.d.Proxy.Address)
		c.Hint = "Set the Docker host's address under Settings → General → Host for project links."
		// The UI labels this action itself ("Use <host>").
		if v := suggestion["ip"]; v != "" {
			c.Action = &CheckAction{Kind: "setPublicHost", Value: v}
		} else if v := suggestion["hostname"]; v != "" {
			c.Action = &CheckAction{Kind: "setPublicHost", Value: v}
		}
	case host != "":
		c.Status, c.Detail = checkOK, "Links use "+host
	default:
		c.Status, c.Detail = checkOK, "Links use the address in the browser."
	}
	return []Check{c}
}

func (a *API) checkProxyPorts(context.Context) []Check {
	c := Check{ID: "network.proxy", Category: catNetwork, Title: "Embedded proxy (domains)", Docs: "domains-and-https"}
	p := a.d.Proxy
	switch {
	case p == nil || !p.Enabled:
		c.Status, c.Detail = checkInfo, "Disabled. Projects are reachable by port only."
		c.Hint = "Enable the proxy (ENVORYX_PROXY_HTTP/HTTPS) to open projects as <project>.<domain> with HTTPS."
	case p.InDocker && p.HTTPPort == 0 && p.HTTPSPort == 0 && p.Address == "":
		c.Status = checkWarning
		c.Detail = "The proxy listens inside the container, but neither port 80 nor 443 is published on the host."
		c.Hint = "Add port mappings 80:80 and 443:443 (or other free host ports) to the Envoryx container and restart it. Until then project domains do not work."
	case p.Address != "":
		c.Status, c.Detail = checkOK, fmt.Sprintf("Reachable directly at %s (ports 80/443)", p.Address)
	default:
		c.Status, c.Detail = checkOK, fmt.Sprintf("Published on host ports %d (HTTP) and %d (HTTPS)", p.HTTPPort, p.HTTPSPort)
	}
	return []Check{c}
}

// checkDNS resolves a name under the base domain the way Envoryx's own resolver does and
// compares it with where the proxy is. Devices often use a different DNS server (an ad
// blocker with the wildcard rewrite while the container asks the router), so a failure
// here is only a note; the browser check in the diagnostics tab has the final say.
func (a *API) checkDNS(ctx context.Context) []Check {
	c := Check{ID: "network.dns", Category: catNetwork, Title: "Wildcard DNS as seen by Envoryx", Docs: "names"}
	if a.d.Proxy == nil || !a.d.Proxy.Enabled {
		c.Status, c.Detail = checkInfo, "Not needed while the proxy is disabled."
		return []Check{c}
	}
	base := a.d.Projects.BaseDomain(ctx)
	name := project.ProbeHostname(base)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, name)
	resolver := systemResolvers()
	if err != nil {
		c.Status = checkInfo
		c.Detail = fmt.Sprintf("*.%s does not resolve from inside the Envoryx container (its DNS: %s). Your devices may use a different DNS server – the check from this browser above is what counts.", base, resolver)
		c.Hint = "If project domains do not open on your devices either: point every name under the base domain at the proxy – a DNS rewrite for the wildcard domain in AdGuard Home or Pi-hole, a wildcard record in your router or DNS server, or hosts-file entries per project."
		return []Check{c}
	}
	expected := a.d.Proxy.Address
	if expected == "" {
		expected = a.publicHost(ctx)
	}
	if expected != "" {
		for _, addr := range addrs {
			if addr == expected {
				c.Status, c.Detail = checkOK, fmt.Sprintf("*.%s → %s (DNS: %s)", base, addr, resolver)
				return []Check{c}
			}
		}
		c.Status = checkWarning
		c.Detail = fmt.Sprintf("*.%s resolves to %s, but the proxy is at %s (DNS: %s).", base, strings.Join(addrs, ", "), expected, resolver)
		c.Hint = "Update the wildcard DNS entry so project domains reach the proxy."
		return []Check{c}
	}
	c.Status, c.Detail = checkOK, fmt.Sprintf("*.%s → %s (DNS: %s)", base, strings.Join(addrs, ", "), resolver)
	return []Check{c}
}

// systemResolvers lists the nameservers of /etc/resolv.conf for the DNS check's detail.
func systemResolvers() string {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return "unknown"
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, ", ")
}

func (a *API) checkSSH(context.Context) []Check {
	c := Check{ID: "network.ssh", Category: catNetwork, Title: "SSH for IDEs", Docs: "ide-integration-phpstorm-vs-code"}
	switch {
	case a.d.SSH == nil || !a.d.SSH.Enabled:
		c.Status, c.Detail = checkInfo, "Disabled."
	case a.d.SSH.Port == 0:
		c.Status, c.Detail = checkInfo, "The SSH server runs, but its port is not published on the host; IDEs on other machines cannot connect."
		c.Hint = "Map port 2222 on the Envoryx container to use remote interpreters and SFTP."
	default:
		c.Status, c.Detail = checkOK, fmt.Sprintf("Port %d, fingerprint %s", a.d.SSH.Port, a.d.SSH.Fingerprint)
	}
	return []Check{c}
}

// ---- security ----------------------------------------------------------------------------

func (a *API) checkTLS(context.Context) []Check {
	c := Check{ID: "security.tls", Category: catSecurity, Title: "HTTPS for projects", Docs: "domains-and-https"}
	switch {
	case a.d.Certs == nil:
		c.Status, c.Detail = checkInfo, "Disabled; project domains are served over plain HTTP."
	case a.d.Proxy != nil && a.d.Proxy.HTTPSPort == 0 && a.d.Proxy.Address == "":
		c.Status, c.Detail = checkWarning, "A CA exists, but port 443 is not published."
		c.Hint = "Map port 443 so browsers can use HTTPS."
	default:
		c.Status, c.Detail = checkOK, "Local CA issues certificates for project domains."
		c.Hint = "Install the CA certificate (Settings → Domains & HTTPS) on each device once to avoid browser warnings."
	}
	return []Check{c}
}

// ---- maintenance -------------------------------------------------------------------------

func (a *API) checkUpdate(context.Context) []Check {
	c := Check{ID: "maintenance.update", Category: catMaintenance, Title: "Envoryx version", Docs: "updating"}
	st := a.d.Updates.Status()
	switch {
	case !st.Enabled:
		c.Status, c.Detail = checkInfo, st.Current+" – update check disabled"
	case st.Available:
		c.Status, c.Detail = checkInfo, fmt.Sprintf("%s available, running %s", st.Latest, st.Current)
		c.Hint = "Update the container (on Unraid: Docker → Check for Updates → Apply). An instance backup is taken automatically before the database migrates."
	case st.Error != "" && st.Latest == "":
		c.Status, c.Detail = checkInfo, st.Current+" – the release check has not succeeded yet: "+st.Error
	default:
		c.Status, c.Detail = checkOK, st.Current+" is the newest release"
		if !st.Release {
			c.Detail = st.Current + " (development build)"
		}
	}
	return []Check{c}
}

func (a *API) checkReconcile(context.Context) []Check {
	c := Check{ID: "maintenance.reconcile", Category: catMaintenance, Title: "Projects vs. Docker"}
	r := a.d.Projects.LastReport()
	switch {
	case r.At.IsZero():
		c.Status, c.Detail = checkInfo, "No reconcile run yet."
	case r.Error != "":
		c.Status, c.Detail = checkWarning, r.Error
		c.Hint = "The comparison between the database and Docker failed; usually the engine was unreachable. It runs again every 30 seconds."
	case len(r.Issues) > 0 || len(r.Orphans) > 0:
		c.Status = checkWarning
		var parts []string
		for _, i := range r.Issues {
			parts = append(parts, i.ProjectName+": "+i.Message)
		}
		if len(r.Orphans) > 0 {
			parts = append(parts, fmt.Sprintf("%d orphaned Docker resources carry Envoryx labels but belong to no project", len(r.Orphans)))
		}
		c.Detail = strings.Join(parts, " · ")
		c.Hint = "Open the affected projects. Orphaned containers and networks are removed automatically within a minute; orphaned volumes are listed on the Docker page and can be removed there."
		c.Action = &CheckAction{Kind: "link", Value: "/docker", Label: "Open Docker page"}
	default:
		c.Status, c.Detail = checkOK, fmt.Sprintf("%d projects consistent with Docker (checked %s)", r.Projects, r.At.Format("15:04:05"))
	}
	return []Check{c}
}

func (a *API) checkNotifications(context.Context) []Check {
	c := Check{ID: "maintenance.notifications", Category: catMaintenance, Title: "Notifications", Docs: "notifications"}
	if a.d.Notify == nil {
		c.Status, c.Detail = checkInfo, "Unavailable."
		return []Check{c}
	}
	st := a.d.Notify.Status()
	switch {
	case !st.Config.Enabled:
		c.Status, c.Detail = checkInfo, "No channel configured. Failed backups, unhealthy projects and low disk space go unnoticed."
		c.Hint = "Configure ntfy, Telegram, Discord, Slack, e-mail or a webhook under Settings → Notifications."
		c.Action = &CheckAction{Kind: "settingsTab", Value: "notifications", Label: "Configure"}
	case st.LastError != "":
		c.Status, c.Detail = checkWarning, st.Config.Provider+": last delivery failed – "+st.LastError
		c.Hint = "Send a test notification to see the current error."
	default:
		c.Status, c.Detail = checkOK, st.Config.Provider
		if st.LastSent != nil {
			c.Detail += ", last sent " + st.LastSent.Format("2006-01-02 15:04")
		}
	}
	return []Check{c}
}
