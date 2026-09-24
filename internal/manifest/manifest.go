// Package manifest reads and writes envoryx.yml, the project manifest that lives in a
// project's repository: runtimes, services, domains, environment, workers and cron jobs
// – everything needed to bring the same environment up again from a fresh clone.
//
// The package knows the file format only. Whether a version exists or a PHP extension is
// available is decided by the project package when it turns a manifest into a request,
// so the server that applies the manifest is the one that judges it.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/envoryx/envoryx/internal/validate"
)

// FileName is the manifest's name in the project root.
const FileName = "envoryx.yml"

// CurrentVersion is the format version this Envoryx writes and understands.
const CurrentVersion = 1

// MaxSize bounds a manifest; a real one is a few hundred bytes.
const MaxSize = 64 << 10

// Manifest is the desired state of a project as it is kept in the repository. Nil
// sections are absent; "redis: true" is a Service with every field at its default.
type Manifest struct {
	Version int    `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
	Docroot string `yaml:"docroot,omitempty"`

	Web    *Web    `yaml:"web,omitempty"`
	PHP    *PHP    `yaml:"php,omitempty"`
	Node   *Node   `yaml:"node,omitempty"`
	Python *Python `yaml:"python,omitempty"`

	Database    *Database `yaml:"database,omitempty"`
	Redis       *Service  `yaml:"redis,omitempty"`
	Memcached   *Service  `yaml:"memcached,omitempty"`
	Mailpit     *Service  `yaml:"mailpit,omitempty"`
	RabbitMQ    *Service  `yaml:"rabbitmq,omitempty"`
	Meilisearch *Service  `yaml:"meilisearch,omitempty"`
	Typesense   *Service  `yaml:"typesense,omitempty"`
	OpenSearch  *Service  `yaml:"opensearch,omitempty"`
	Storage     *Storage  `yaml:"storage,omitempty"`

	// Domains are extra host names next to the derived <slug>.<base domain>.
	Domains []string `yaml:"domains,omitempty"`
	// Env are plain environment variables. Secrets names variables whose values never go
	// into the repository: they are asked for (or left empty) when the project is created
	// and kept as they are afterwards.
	Env     map[string]string `yaml:"env,omitempty"`
	Secrets []string          `yaml:"secrets,omitempty"`

	Workers []Worker  `yaml:"workers,omitempty"`
	Cron    []CronJob `yaml:"cron,omitempty"`

	// Limits cap CPU, memory and processes of every container.
	Limits *Limits `yaml:"limits,omitempty"`
}

// Limits are the resource limits of the application containers (web server, PHP, Node,
// Python, workers) and of the services (database, caches, search, storage), each per
// container, plus the process limit of every container.
type Limits struct {
	App      *LimitSet `yaml:"app,omitempty"`
	Services *LimitSet `yaml:"services,omitempty"`
	Pids     int       `yaml:"pids,omitempty"`
}

// LimitSet is one group's limits: cores (1.5) and memory with a unit (512M, 2G).
type LimitSet struct {
	CPUs   float64 `yaml:"cpus,omitempty"`
	Memory string  `yaml:"memory,omitempty"`
}

// MemoryMB parses Memory: a number of MiB, or a number with M/MB/MiB or G/GB/GiB.
func (l LimitSet) MemoryMB() (int, error) {
	v := strings.ToUpper(strings.TrimSpace(l.Memory))
	if v == "" {
		return 0, nil
	}
	mult := 1.0
	for _, suffix := range []struct {
		s string
		m float64
	}{{"GIB", 1024}, {"GB", 1024}, {"G", 1024}, {"MIB", 1}, {"MB", 1}, {"M", 1}} {
		if strings.HasSuffix(v, suffix.s) {
			v, mult = strings.TrimSpace(strings.TrimSuffix(v, suffix.s)), suffix.m
			break
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: memory %q is not a size like 512M or 2G", validate.ErrInvalid, l.Memory)
	}
	return int(n*mult + 0.5), nil
}

// FormatMemory writes MiB the way the file shows it: 2G, 1536M.
func FormatMemory(mb int) string {
	if mb == 0 {
		return ""
	}
	if mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + "G"
	}
	return strconv.Itoa(mb) + "M"
}

// Web selects the web server.
type Web struct {
	Server      string `yaml:"server,omitempty"` // caddy, apache, nginx
	Version     string `yaml:"version,omitempty"`
	SPAFallback bool   `yaml:"spaFallback,omitempty"`
}

// PHP is the PHP runtime with its php.ini settings. Empty fields take Envoryx's defaults.
type PHP struct {
	Version           string   `yaml:"version,omitempty"`
	Extensions        []string `yaml:"extensions,omitempty"`
	MemoryLimit       string   `yaml:"memoryLimit,omitempty"`
	UploadMaxFilesize string   `yaml:"uploadMaxFilesize,omitempty"`
	PostMaxSize       string   `yaml:"postMaxSize,omitempty"`
	MaxExecutionTime  int      `yaml:"maxExecutionTime,omitempty"`
	// DisplayErrors defaults to true, so it needs a pointer to be switched off.
	DisplayErrors  *bool  `yaml:"displayErrors,omitempty"`
	ErrorReporting string `yaml:"errorReporting,omitempty"`
	Xdebug         bool   `yaml:"xdebug,omitempty"`
	XdebugMode     string `yaml:"xdebugMode,omitempty"`
	XdebugIDEKey   string `yaml:"xdebugIdeKey,omitempty"`
}

// Node is the Node.js toolchain and its optional dev server.
type Node struct {
	Version        string `yaml:"version,omitempty"`
	DevServer      bool   `yaml:"devServer,omitempty"`
	Mode           string `yaml:"mode,omitempty"`
	PackageManager string `yaml:"packageManager,omitempty"`
	Script         string `yaml:"script,omitempty"`
	BuildScript    string `yaml:"buildScript,omitempty"`
	Port           int    `yaml:"port,omitempty"`
	Preset         string `yaml:"preset,omitempty"`
	Inspect        bool   `yaml:"inspect,omitempty"`
	InspectPort    int    `yaml:"inspectPort,omitempty"`
}

// Python is the Python runtime and its optional application server.
type Python struct {
	Version   string `yaml:"version,omitempty"`
	Server    bool   `yaml:"server,omitempty"`
	Mode      string `yaml:"mode,omitempty"`
	Preset    string `yaml:"preset,omitempty"`
	App       string `yaml:"app,omitempty"`
	Port      int    `yaml:"port,omitempty"`
	Debug     bool   `yaml:"debug,omitempty"`
	DebugPort int    `yaml:"debugPort,omitempty"`
}

// Database selects the database server.
type Database struct {
	Type       string `yaml:"type,omitempty"` // mariadb, mysql, postgres, mongodb
	Version    string `yaml:"version,omitempty"`
	ExposePort bool   `yaml:"exposePort,omitempty"`
}

// Service is an auxiliary service. In the file it is either "true" or a mapping.
type Service struct {
	Version    string `yaml:"version,omitempty"`
	ExposePort bool   `yaml:"exposePort,omitempty"`
	// Dashboards adds OpenSearch Dashboards (OpenSearch only).
	Dashboards bool `yaml:"dashboards,omitempty"`
}

// Storage is the S3-compatible object storage. In the file it is either "true" or a
// mapping; PublicRead defaults to true.
type Storage struct {
	Version    string `yaml:"version,omitempty"`
	PublicRead *bool  `yaml:"publicRead,omitempty"`
}

// Worker is a long-running process from the worker catalogue.
type Worker struct {
	Name   string `yaml:"name"`
	Preset string `yaml:"preset"`
	Arg    string `yaml:"arg,omitempty"`
	// Enabled defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// CronJob is a command run on a schedule.
type CronJob struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"`
	Runtime  string `yaml:"runtime,omitempty"` // php, node or python; default: the app's
	Command  string `yaml:"command"`
	// Timeout is a Go duration ("10m", "1h"); empty is Envoryx's default.
	Timeout string `yaml:"timeout,omitempty"`
	// Enabled defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled reports the effective state of a worker.
func (w Worker) IsEnabled() bool { return w.Enabled == nil || *w.Enabled }

// IsEnabled reports the effective state of a cron job.
func (c CronJob) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// TimeoutDuration parses Timeout; 0 means the default.
func (c CronJob) TimeoutDuration() (time.Duration, error) {
	if strings.TrimSpace(c.Timeout) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.Timeout))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: cron job %q: timeout %q is not a duration like 10m or 1h", validate.ErrInvalid, c.Name, c.Timeout)
	}
	return d, nil
}

// IsPublicRead returns PublicRead with its default.
func (s Storage) IsPublicRead() bool { return s.PublicRead == nil || *s.PublicRead }

// UnmarshalYAML accepts "redis: true" next to the mapping form. "redis: false" is
// rejected: leaving the key out is how a service is absent.
func (s *Service) UnmarshalYAML(n *yaml.Node) error {
	if on, ok, err := boolScalar(n); ok {
		if err != nil {
			return err
		}
		if !on {
			return fmt.Errorf("line %d: leave a service out instead of setting it to false", n.Line)
		}
		*s = Service{}
		return nil
	}
	type plain Service
	return decodeStrict(n, (*plain)(s))
}

// MarshalYAML writes a service without settings as "true".
func (s Service) MarshalYAML() (any, error) {
	if s == (Service{}) {
		return true, nil
	}
	type plain Service
	return plain(s), nil
}

// UnmarshalYAML accepts "storage: true" next to the mapping form.
func (s *Storage) UnmarshalYAML(n *yaml.Node) error {
	if on, ok, err := boolScalar(n); ok {
		if err != nil {
			return err
		}
		if !on {
			return fmt.Errorf("line %d: leave storage out instead of setting it to false", n.Line)
		}
		*s = Storage{}
		return nil
	}
	type plain Storage
	return decodeStrict(n, (*plain)(s))
}

// MarshalYAML writes storage without settings as "true".
func (s Storage) MarshalYAML() (any, error) {
	if s.Version == "" && s.PublicRead == nil {
		return true, nil
	}
	type plain Storage
	return plain(s), nil
}

func boolScalar(n *yaml.Node) (on, ok bool, err error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
		return false, false, nil
	}
	var b bool
	if err := n.Decode(&b); err != nil {
		return false, true, err
	}
	return b, true, nil
}

// decodeStrict decodes a mapping node and refuses keys the target does not have –
// Node.Decode does not inherit the decoder's KnownFields setting.
func decodeStrict(n *yaml.Node, out any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		// Line numbers refer to the re-encoded fragment; name the section's line instead.
		return fmt.Errorf("line %d: %s", n.Line, yamlError(err))
	}
	return nil
}

var notFoundRe = regexp.MustCompile(`field (\S+) not found in type \S+`)

// yamlError turns the decoder's wording into something a manifest author can act on:
// "line 2: unknown key redsi" instead of "field redsi not found in type manifest.Manifest".
func yamlError(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs := make([]string, len(te.Errors))
		for i, e := range te.Errors {
			msgs[i] = notFoundRe.ReplaceAllString(e, "unknown key $1")
		}
		return strings.Join(msgs, "; ")
	}
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

// Parse reads and checks a manifest. Unknown keys are errors: a typo must not silently
// drop a service.
func Parse(data []byte) (Manifest, error) {
	if len(data) > MaxSize {
		return Manifest{}, fmt.Errorf("%w: %s is larger than %d KiB", validate.ErrInvalid, FileName, MaxSize>>10)
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("%w: %s is empty", validate.ErrInvalid, FileName)
		}
		return Manifest{}, fmt.Errorf("%w: %s: %s", validate.ErrInvalid, FileName, yamlError(err))
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: %s holds more than one document", validate.ErrInvalid, FileName)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks what the file format alone can tell: the version, names and the
// consistency between sections.
func (m Manifest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s: %s", validate.ErrInvalid, FileName, fmt.Sprintf(format, a...))
	}
	switch {
	case m.Version == 0:
		return bad("version is missing (write \"version: %d\")", CurrentVersion)
	case m.Version > CurrentVersion:
		return bad("version %d is newer than this Envoryx understands (%d); update Envoryx", m.Version, CurrentVersion)
	case m.Version < 0:
		return bad("version %d is invalid", m.Version)
	}
	if m.Name != "" {
		if err := validate.ProjectName(m.Name); err != nil {
			return bad("name: %v", unwrapInvalid(err))
		}
	}
	for _, s := range []*Service{m.Redis, m.Memcached, m.Mailpit, m.RabbitMQ, m.Meilisearch, m.Typesense} {
		if s != nil && s.Dashboards {
			return bad("dashboards belongs to opensearch")
		}
	}
	if m.Web != nil && m.Web.SPAFallback && m.PHP != nil {
		return bad("web.spaFallback needs a project without PHP; the front controller handles unknown paths")
	}
	seenHost := map[string]bool{}
	for _, h := range m.Domains {
		h = validate.NormalizeHostname(h)
		if err := validate.Hostname(h); err != nil {
			return bad("domains: %v", unwrapInvalid(err))
		}
		if seenHost[h] {
			return bad("domains: %s is listed twice", h)
		}
		seenHost[h] = true
	}
	for k, v := range m.Env {
		if err := validate.EnvKey(k); err != nil {
			return bad("env: %v", unwrapInvalid(err))
		}
		if err := validate.EnvValue(v); err != nil {
			return bad("env %s: %v", k, unwrapInvalid(err))
		}
	}
	seenSecret := map[string]bool{}
	for _, k := range m.Secrets {
		if err := validate.EnvKey(k); err != nil {
			return bad("secrets: %v", unwrapInvalid(err))
		}
		if _, ok := m.Env[k]; ok {
			return bad("%s is listed under env and under secrets", k)
		}
		if seenSecret[k] {
			return bad("secrets: %s is listed twice", k)
		}
		seenSecret[k] = true
	}
	if m.Limits != nil {
		for name, set := range map[string]*LimitSet{"app": m.Limits.App, "services": m.Limits.Services} {
			if set == nil {
				continue
			}
			if _, err := set.MemoryMB(); err != nil {
				return bad("limits.%s: %v", name, unwrapInvalid(err))
			}
			if set.CPUs < 0 {
				return bad("limits.%s.cpus must not be negative", name)
			}
		}
	}
	seenWorker := map[string]bool{}
	for i, w := range m.Workers {
		if strings.TrimSpace(w.Name) == "" || strings.TrimSpace(w.Preset) == "" {
			return bad("workers[%d] needs a name and a preset", i)
		}
		name := strings.ToLower(strings.TrimSpace(w.Name))
		if seenWorker[name] {
			return bad("workers: %s is listed twice", name)
		}
		seenWorker[name] = true
	}
	seenCron := map[string]bool{}
	for i, c := range m.Cron {
		if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Schedule) == "" || strings.TrimSpace(c.Command) == "" {
			return bad("cron[%d] needs a name, a schedule and a command", i)
		}
		if seenCron[strings.TrimSpace(c.Name)] {
			return bad("cron: %s is listed twice", c.Name)
		}
		seenCron[strings.TrimSpace(c.Name)] = true
		if _, err := c.TimeoutDuration(); err != nil {
			return err
		}
	}
	return nil
}

// unwrapInvalid drops the "invalid input: " prefix of a validate error that is wrapped
// again with the file name.
func unwrapInvalid(err error) string {
	return strings.TrimPrefix(err.Error(), validate.ErrInvalid.Error()+": ")
}

// header opens every file Envoryx writes.
const header = `# Envoryx project manifest – commit it with the code.
# "envoryx up" in a clone of this repository creates the project exactly like this or
# brings an existing one in line. Host ports are assigned by the server; secret values
# never belong here, only their names under "secrets".
`

// Marshal writes a manifest with Envoryx's header. Map keys come out sorted, lists in
// their order, so the same project always produces the same file.
func Marshal(m Manifest) ([]byte, error) {
	if m.Version == 0 {
		m.Version = CurrentVersion
	}
	m.Secrets = slices.Clone(m.Secrets)
	slices.Sort(m.Secrets)
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
