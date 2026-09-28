// Package addon reads addon definitions: services described in a YAML file instead of
// Envoryx's code. An addon is one container per project (image, environment, data
// volumes, a port, optionally a web UI through the proxy) plus the variables it hands
// the application. A definition cannot ask for privileged mode, host networking, host
// directories or devices: the format has no field for them, so every addon runs with
// named volumes on the project network only.
package addon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"go.yaml.in/yaml/v3"

	"github.com/envoryx/envoryx/internal/validate"
)

// MaxSize caps a definition file.
const MaxSize = 64 << 10

// Definition is one addon.
type Definition struct {
	// Name identifies the addon: the service is addon-<name>, the container
	// envoryx-<slug>-addon-<name>.
	Name        string `yaml:"name" json:"name"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Homepage    string `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	// Versions to choose from; the default (or the first) is taken when none is given.
	Versions []Version `yaml:"versions" json:"versions"`
	// Hostname is the container's name on the project network; empty means Name.
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	// Port is the port the service listens on (0: none), the target of the web UI, the
	// host port and {{port}}.
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// WebUI routes <slug>-<name>.<base domain> through the proxy to Port.
	WebUI bool `yaml:"webUI,omitempty" json:"webUI,omitempty"`
	// PublishPort offers a host port for Port (for desktop clients).
	PublishPort bool `yaml:"publishPort,omitempty" json:"publishPort,omitempty"`
	// Command replaces the image's command.
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// User runs the container as this user (inside the container).
	User string `yaml:"user,omitempty" json:"user,omitempty"`
	// Env is the container's environment; values are templates.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Secrets are generated per project (24 random characters) and available as
	// {{secret.<name>}}.
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// Volumes keep data across container recreation.
	Volumes []Volume `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	// Healthcheck is Docker's health check.
	Healthcheck *Healthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	// Inject is the environment the application containers get; values are templates.
	Inject map[string]string `yaml:"inject,omitempty" json:"inject,omitempty"`
	// Credentials are shown in the UI (a login, an API key); values are templates.
	Credentials []Credential `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// Version is one selectable image.
type Version struct {
	Version string `yaml:"version" json:"version"`
	Image   string `yaml:"image" json:"image"`
	Default bool   `yaml:"default,omitempty" json:"default,omitempty"`
}

// Volume is a named volume mounted at Path.
type Volume struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
	// NoBackup leaves the volume out of backups (caches).
	NoBackup bool `yaml:"noBackup,omitempty" json:"noBackup,omitempty"`
}

// Healthcheck is a Docker health check; durations are Go durations ("10s").
type Healthcheck struct {
	Test        []string `yaml:"test" json:"test"`
	Interval    string   `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	StartPeriod string   `yaml:"startPeriod,omitempty" json:"startPeriod,omitempty"`
	Retries     int      `yaml:"retries,omitempty" json:"retries,omitempty"`
}

// Credential is a labelled value shown in the UI.
type Credential struct {
	Label string `yaml:"label" json:"label"`
	Value string `yaml:"value" json:"value"`
	// Secret masks the value until it is revealed.
	Secret bool `yaml:"secret,omitempty" json:"secret,omitempty"`
}

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,28}[a-z0-9]$`)
	secretRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	tmplRe   = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.]+)\s*\}\}`)
	// containerEnvRe is looser than an application variable: images like Elasticsearch
	// read settings such as discovery.type from the environment.
	containerEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,127}$`)
)

// reserved are names of Envoryx's own services and network aliases.
var reserved = map[string]bool{
	"web": true, "php": true, "node": true, "python": true, "go": true, "ruby": true, "java": true, "dotnet": true,
	"database": true, "db": true, "mysql": true, "mariadb": true, "postgres": true, "mongodb": true, "mongo": true,
	"redis": true, "memcached": true, "mailpit": true, "mail": true, "smtp": true, "rabbitmq": true, "amqp": true,
	"meilisearch": true, "typesense": true, "opensearch": true, "opensearch-dashboards": true, "ollama": true,
	"storage": true, "s3": true, "dev": true, "envoryx": true, "share": true, "worker": true, "cron": true,
	"localhost": true, "app": true,
}

// Variables every template may use, besides secret.<name>.
var templateVars = []string{
	"host", "port", "url",
	"project.slug", "project.name", "project.url",
	"database.type", "database.host", "database.port", "database.name", "database.user", "database.password",
}

// Parse reads and validates a definition.
func Parse(data []byte) (Definition, error) {
	if len(data) > MaxSize {
		return Definition{}, fmt.Errorf("%w: the addon file is larger than %d KB", validate.ErrInvalid, MaxSize>>10)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Definition
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return Definition{}, fmt.Errorf("%w: the addon file is empty", validate.ErrInvalid)
		}
		return Definition{}, fmt.Errorf("%w: addon file: %v", validate.ErrInvalid, err)
	}
	if err := d.Validate(); err != nil {
		return Definition{}, err
	}
	return d, nil
}

// Marshal writes a definition as YAML.
func Marshal(d Definition) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// ValidName checks an addon name.
func ValidName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "--") {
		return fmt.Errorf("%w: addon name %q: 2 to 30 lower-case letters, digits and single dashes, starting with a letter", validate.ErrInvalid, name)
	}
	if reserved[name] {
		return fmt.Errorf("%w: addon name %q is taken by Envoryx", validate.ErrInvalid, name)
	}
	return nil
}

// Validate checks a definition, including every template it holds.
func (d *Definition) Validate() error {
	bad := func(format string, a ...any) error {
		prefix := "addon"
		if d.Name != "" {
			prefix = "addon " + d.Name
		}
		return fmt.Errorf("%w: %s: %s", validate.ErrInvalid, prefix, fmt.Sprintf(format, a...))
	}
	if err := ValidName(d.Name); err != nil {
		return err
	}
	if strings.TrimSpace(d.Title) == "" {
		d.Title = d.Name
	}
	if len(d.Title) > 60 || len(d.Description) > 500 || len(d.Homepage) > 300 {
		return bad("title, description or homepage is too long")
	}
	if d.Homepage != "" && !strings.HasPrefix(d.Homepage, "https://") && !strings.HasPrefix(d.Homepage, "http://") {
		return bad("homepage must be an http(s) URL")
	}
	if len(d.Versions) == 0 {
		return bad("versions: at least one version with an image is needed")
	}
	seen, defaults := map[string]bool{}, 0
	for _, v := range d.Versions {
		if v.Version == "" || len(v.Version) > 40 || strings.ContainsAny(v.Version, " /\\") {
			return bad("version %q is invalid", v.Version)
		}
		if seen[v.Version] {
			return bad("version %s is listed twice", v.Version)
		}
		seen[v.Version] = true
		if _, err := reference.ParseNormalizedNamed(v.Image); err != nil || strings.HasPrefix(v.Image, "envoryx-") {
			return bad("version %s: %q is not an image reference", v.Version, v.Image)
		}
		if v.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return bad("more than one version is the default")
	}
	if d.Hostname != "" {
		if !nameRe.MatchString(d.Hostname) || reserved[d.Hostname] {
			return bad("hostname %q is invalid or taken by Envoryx", d.Hostname)
		}
	}
	if d.Port < 0 || d.Port > 65535 {
		return bad("port %d is out of range", d.Port)
	}
	if (d.WebUI || d.PublishPort) && d.Port == 0 {
		return bad("webUI and publishPort need a port")
	}
	if len(d.Command) > 50 {
		return bad("command is too long")
	}
	if len(d.User) > 64 || strings.ContainsAny(d.User, " \t\n") {
		return bad("user %q is invalid", d.User)
	}
	for _, s := range d.Secrets {
		if !secretRe.MatchString(s) {
			return bad("secret name %q: lower-case letters, digits and underscores", s)
		}
	}
	if len(d.Secrets) != len(uniq(d.Secrets)) {
		return bad("a secret is listed twice")
	}
	vols := map[string]bool{}
	for _, v := range d.Volumes {
		if !secretRe.MatchString(v.Name) || vols[v.Name] {
			return bad("volume name %q is invalid or listed twice", v.Name)
		}
		vols[v.Name] = true
		if !strings.HasPrefix(v.Path, "/") || strings.Contains(v.Path, "..") || v.Path == "/" {
			return bad("volume %s: the path must be an absolute directory in the container", v.Name)
		}
	}
	if h := d.Healthcheck; h != nil {
		if len(h.Test) == 0 {
			return bad("healthcheck: test is empty")
		}
		for _, s := range []string{h.Interval, h.Timeout, h.StartPeriod} {
			if s == "" {
				continue
			}
			if dur, err := time.ParseDuration(s); err != nil || dur <= 0 || dur > time.Hour {
				return bad("healthcheck: %q is not a duration like 10s", s)
			}
		}
		if h.Retries < 0 || h.Retries > 100 {
			return bad("healthcheck: retries out of range")
		}
	}
	for k, v := range d.Env {
		if !containerEnvRe.MatchString(k) {
			return bad("env %q is not a variable name", k)
		}
		if err := validate.EnvValue(v); err != nil {
			return bad("env %s: %v", k, err)
		}
		if err := d.checkTemplate(v); err != nil {
			return bad("env %s: %v", k, err)
		}
	}
	for k, v := range d.Inject {
		if err := validate.EnvKey(k); err != nil {
			return bad("inject %s: %v", k, err)
		}
		if err := validate.EnvValue(v); err != nil {
			return bad("inject %s: %v", k, err)
		}
		if err := d.checkTemplate(v); err != nil {
			return bad("inject %s: %v", k, err)
		}
	}
	for _, c := range d.Credentials {
		if strings.TrimSpace(c.Label) == "" {
			return bad("a credential has no label")
		}
		if err := d.checkTemplate(c.Value); err != nil {
			return bad("credential %s: %v", c.Label, err)
		}
	}
	for _, s := range d.Command {
		if err := d.checkTemplate(s); err != nil {
			return bad("command: %v", err)
		}
	}
	return nil
}

func uniq(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// checkTemplate fails for a placeholder the addon cannot fill.
func (d *Definition) checkTemplate(s string) error {
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		name := m[1]
		if secret, ok := strings.CutPrefix(name, "secret."); ok {
			if !slices.Contains(d.Secrets, secret) {
				return fmt.Errorf("{{%s}} names a secret the addon does not declare", name)
			}
			continue
		}
		if !slices.Contains(templateVars, name) {
			return fmt.Errorf("{{%s}} is unknown (known: %s and secret.<name>)", name, strings.Join(templateVars, ", "))
		}
	}
	return nil
}

// HostnameOrName is the addon's name on the project network.
func (d Definition) HostnameOrName() string {
	if d.Hostname != "" {
		return d.Hostname
	}
	return d.Name
}

// Resolve returns the version to run: the requested one, else the default, else the
// first.
func (d Definition) Resolve(version string) (Version, error) {
	if version != "" {
		for _, v := range d.Versions {
			if v.Version == version {
				return v, nil
			}
		}
		return Version{}, fmt.Errorf("%w: addon %s has no version %q", validate.ErrInvalid, d.Name, version)
	}
	for _, v := range d.Versions {
		if v.Default {
			return v, nil
		}
	}
	return d.Versions[0], nil
}

// Render fills a template's placeholders from vars; an unknown one stays empty.
func Render(s string, vars map[string]string) string {
	return tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		return vars[tmplRe.FindStringSubmatch(m)[1]]
	})
}

// RenderMap renders every value of a map, as sorted KEY=value strings.
func RenderMap(m map[string]string, vars map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+Render(m[k], vars))
	}
	return out
}

// Durations returns the health check's durations (zero for unset ones).
func (h Healthcheck) Durations() (interval, timeout, startPeriod time.Duration) {
	parse := func(s string) time.Duration {
		d, _ := time.ParseDuration(s)
		return d
	}
	return parse(h.Interval), parse(h.Timeout), parse(h.StartPeriod)
}
