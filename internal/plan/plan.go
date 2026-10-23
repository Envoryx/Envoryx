// Package plan holds the limits a hoster sets for an instance it manages: how many
// projects and users, how much CPU, memory and disk, which features. A managed instance
// reads its plan from a file in the config directory (written by the fleet manager's
// connection later on); without the file nothing is limited and Envoryx behaves as it
// always did.
//
// The plan is only worth something when the customer has no root on the host: whoever
// can edit the file or reach the Docker socket directly is not bound by it.
package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
)

// File is the plan's name in the config directory.
const File = "plan.json"

// Feature names a part of Envoryx a plan can switch off.
type Feature string

const (
	FeatureAddons           Feature = "addons"
	FeatureCustomImages     Feature = "customImages"
	FeatureBranchEnvs       Feature = "branchEnvironments"
	FeatureExternalServices Feature = "externalServices"
	FeatureOffsite          Feature = "offsite"
	FeatureIDEGateway       Feature = "ideGateway"
	// FeatureEOLVersions are versions past their end of life (PHP 8.1), which get no
	// security fixes; FeaturePreviewVersions the pre-releases (PHP 8.6 RC). Switched
	// off, no project gets one newly; projects that run one keep it.
	FeatureEOLVersions     Feature = "eolVersions"
	FeaturePreviewVersions Feature = "previewVersions"
	// FeaturePublicProjects are projects open to everyone who knows their address.
	// Switched off, every project lets in only people signed in to Envoryx (or with a
	// guest link). FeaturePublicShares are shares on a public tunnel address, which skip
	// that sign-in on purpose.
	FeaturePublicProjects Feature = "publicProjects"
	FeaturePublicShares   Feature = "publicShares"
)

// Features lists every feature a plan can switch off, in display order.
var Features = []Feature{FeatureAddons, FeatureCustomImages, FeatureBranchEnvs, FeatureExternalServices, FeatureOffsite, FeatureIDEGateway, FeatureEOLVersions, FeaturePreviewVersions, FeaturePublicProjects, FeaturePublicShares}

// Plan is what a hoster allows an instance. Zero values mean "no limit".
type Plan struct {
	// Name is shown to the instance's users ("Starter", "Team").
	Name   string `json:"name,omitempty"`
	Limits Limits `json:"limits"`
	// Runtimes, when set, are the only runtimes new projects may use (php, node, python,
	// ruby, go, java, dotnet, static).
	Runtimes []string `json:"runtimes,omitempty"`
	// Services, when set, are the only services and database engines projects may add
	// (ServiceNames). Projects that run another one keep it.
	Services []string `json:"services,omitempty"`
	// Disabled are the features the plan switches off.
	Disabled []Feature `json:"disabled,omitempty"`
	// Settings are instance settings the hoster fixes, by their name in the settings API
	// (LockableSettings): they apply with these values and the instance's admins see them
	// but can't change them.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Offsite is the hoster's offsite target: the daily instance backup (and with
	// Projects the scheduled project backups) go there, into a folder of the instance's
	// own below Prefix. The instance's admins see it but can't change it.
	Offsite *OffsiteTarget `json:"offsite,omitempty"`
}

// OffsiteTarget is an offsite target in a plan, with the field names of Envoryx's
// offsite targets. The instance checks the details when it takes the plan over.
type OffsiteTarget struct {
	// Type is s3, sftp or webdav.
	Type string `json:"type"`
	// Prefix is the folder below which every instance gets its own (default envoryx).
	Prefix string `json:"prefix,omitempty"`
	// InstanceHour is the hour of the daily instance backup, InstanceKeep how many of
	// them stay on the target (0 keeps all).
	InstanceHour int `json:"instanceHour,omitempty"`
	InstanceKeep int `json:"instanceKeep,omitempty"`
	// Projects copies every scheduled project backup too; Keep is how many per project
	// stay (0 keeps all).
	Projects bool `json:"projects,omitempty"`
	Keep     int  `json:"keep,omitempty"`
	// Encrypt seals the archives with Passphrase (age) before they leave the instance.
	Encrypt    bool   `json:"encrypt,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`

	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	AccessKey string `json:"accessKey,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`

	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	HostKey    string `json:"hostKey,omitempty"`

	URL string `json:"url,omitempty"`
}

// Limits are the plan's quotas. Zero means unlimited. CPU and memory are what the
// hoster's VM has; a plan doesn't divide them further.
type Limits struct {
	Projects int `json:"projects,omitempty"`
	Users    int `json:"users,omitempty"`
	// DiskGB caps what the projects take: their directories, volumes and backups. It is
	// measured every few minutes, so it stops new projects and backups once reached
	// rather than any write at the byte. The unit is the GiB (2^30 bytes), as the
	// instance shows it; the JSON name stays diskGb.
	DiskGB float64 `json:"diskGb,omitempty"`
	// RunningProjects caps the projects that run at the same time; a stopped project
	// doesn't count.
	RunningProjects int `json:"runningProjects,omitempty"`
	// Container caps the CPU and memory of every project container: a project may set
	// lower limits, never higher ones or none. ContainerDefault is what a container
	// without a limit of its own gets (Container where it has no value).
	Container        ContainerLimits `json:"container,omitzero"`
	ContainerDefault ContainerLimits `json:"containerDefault,omitzero"`
}

// ContainerLimits are the CPU cores and the memory (MiB) of one container; zero means no
// limit.
type ContainerLimits struct {
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int     `json:"memoryMb,omitempty"`
}

// ServiceNames are the services and database engines a plan's Services can name.
// opensearch includes its dashboards.
var ServiceNames = []string{"mariadb", "mysql", "postgresql", "mongodb", "redis", "memcached", "rabbitmq", "meilisearch", "typesense", "opensearch", "ollama", "mailpit", "storage"}

// LockableSettings are the instance settings a plan can fix.
var LockableSettings = []string{"publicHost", "baseDomain", "forceHttps", "projectsFollowEnvoryx", "sharedIdeBackends", "sharedPackageCache", "xdebugClientHost", "folderViewFolder", "metricsRetentionDays", "newProjectsPrivate", "twoFactorRequired", "logHistory", "imageUpdates"}

// ErrQuota is returned when an action would go beyond the plan.
var ErrQuota = errors.New("not included in the plan")

// Quota formats an ErrQuota.
func Quota(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrQuota, fmt.Sprintf(format, args...))
}

// Load reads a plan file. A missing file is no plan (nil, nil).
func Load(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads and checks a plan.
func Parse(b []byte) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the values a plan carries.
func (p *Plan) Validate() error {
	l := p.Limits
	if l.Projects < 0 || l.Users < 0 || l.RunningProjects < 0 || l.DiskGB < 0 || math.IsNaN(l.DiskGB) || math.IsInf(l.DiskGB, 0) {
		return errors.New("plan: limits can't be negative")
	}
	for _, c := range []ContainerLimits{l.Container, l.ContainerDefault} {
		if c.CPUs < 0 || math.IsNaN(c.CPUs) || math.IsInf(c.CPUs, 0) || c.MemoryMB < 0 || (c.CPUs > 0 && c.CPUs < 0.1) || (c.MemoryMB > 0 && c.MemoryMB < 64) {
			return errors.New("plan: container limits need at least 0.1 cores and 64 MiB")
		}
	}
	if (l.Container.CPUs > 0 && l.ContainerDefault.CPUs > l.Container.CPUs) || (l.Container.MemoryMB > 0 && l.ContainerDefault.MemoryMB > l.Container.MemoryMB) {
		return errors.New("plan: a container default can't be above the container limit")
	}
	for _, s := range p.Services {
		if !slices.Contains(ServiceNames, s) {
			return fmt.Errorf("plan: unknown service %q (possible: %s)", s, strings.Join(ServiceNames, ", "))
		}
	}
	for k, v := range p.Settings {
		if !slices.Contains(LockableSettings, k) {
			return fmt.Errorf("plan: the setting %q can't be fixed (possible: %s)", k, strings.Join(LockableSettings, ", "))
		}
		if !json.Valid(v) {
			return fmt.Errorf("plan: the setting %q has no valid value", k)
		}
	}
	if o := p.Offsite; o != nil {
		if o.Type != "s3" && o.Type != "sftp" && o.Type != "webdav" {
			return fmt.Errorf("plan: unknown offsite target type %q (s3, sftp or webdav)", o.Type)
		}
		if o.InstanceHour < 0 || o.InstanceHour > 23 || o.InstanceKeep < 0 || o.Keep < 0 {
			return errors.New("plan: the offsite target needs an hour from 0 to 23 and counts that aren't negative")
		}
		if o.Encrypt && o.Passphrase == "" {
			return errors.New("plan: an encrypted offsite target needs a passphrase")
		}
	}
	for _, f := range p.Disabled {
		if !slices.Contains(Features, f) {
			return fmt.Errorf("plan: unknown feature %q", f)
		}
	}
	return nil
}

// Allows reports whether the plan includes a feature. A nil plan includes everything.
func (p *Plan) Allows(f Feature) bool {
	return p == nil || !slices.Contains(p.Disabled, f)
}

// RequireFeature returns an ErrQuota when the plan switches f off.
func (p *Plan) RequireFeature(f Feature) error {
	if p.Allows(f) {
		return nil
	}
	return Quota("%s are switched off for this instance", featureNoun[f])
}

var featureNoun = map[Feature]string{
	FeatureAddons:           "addons",
	FeatureCustomImages:     "custom images",
	FeatureBranchEnvs:       "branch environments",
	FeatureExternalServices: "external services",
	FeatureOffsite:          "offsite targets",
	FeatureIDEGateway:       "IDE gateways",
	FeatureEOLVersions:      "end-of-life versions",
	FeaturePreviewVersions:  "preview versions",
	FeaturePublicProjects:   "projects open to everyone",
	FeaturePublicShares:     "public shares",
}

// AllowsRuntime reports whether new projects may use a runtime.
func (p *Plan) AllowsRuntime(rt string) bool {
	return p == nil || len(p.Runtimes) == 0 || slices.Contains(p.Runtimes, rt)
}

// RequireRuntime returns an ErrQuota when the plan leaves rt out.
func (p *Plan) RequireRuntime(rt string) error {
	if p.AllowsRuntime(rt) {
		return nil
	}
	return Quota("the runtime %s is not available on this instance; the plan includes %s", rt, strings.Join(p.Runtimes, ", "))
}

// AllowsService reports whether projects may add a service or database engine (one of
// ServiceNames).
func (p *Plan) AllowsService(name string) bool {
	return p == nil || len(p.Services) == 0 || slices.Contains(p.Services, name)
}

// RequireService returns an ErrQuota when the plan leaves the service name out; label is
// how users know it ("OpenSearch").
func (p *Plan) RequireService(name, label string) error {
	if p.AllowsService(name) {
		return nil
	}
	return Quota("%s is not available on this instance's plan", label)
}

// ContainerLimit is what a container runs with under the plan when its project asks for
// cpus and memoryMB (0 for no limit): the project's value up to the plan's cap, the
// plan's default (or cap) where the project sets none.
func (p *Plan) ContainerLimit(cpus float64, memoryMB int) (float64, int) {
	if p == nil {
		return cpus, memoryMB
	}
	c, d := p.Limits.Container, p.Limits.ContainerDefault
	return capped(cpus, d.CPUs, c.CPUs), capped(memoryMB, d.MemoryMB, c.MemoryMB)
}

// capped is v, or def when v is 0, at most limit (when limit isn't 0).
func capped[T int | float64](v, def, limit T) T {
	if v == 0 {
		v = def
	}
	if limit > 0 && (v == 0 || v > limit) {
		v = limit
	}
	return v
}

// Locked reports whether the plan fixes an instance setting, and its value.
func (p *Plan) Locked(key string) (json.RawMessage, bool) {
	if p == nil {
		return nil, false
	}
	v, ok := p.Settings[key]
	return v, ok
}

// LockedKeys returns the names of the settings the plan fixes, sorted.
func (p *Plan) LockedKeys() []string {
	if p == nil {
		return []string{}
	}
	keys := make([]string, 0, len(p.Settings))
	for k := range p.Settings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// DiskBytes is the disk limit in bytes, 0 for none.
func (p *Plan) DiskBytes() int64 {
	if p == nil {
		return 0
	}
	return int64(p.Limits.DiskGB * (1 << 30))
}
