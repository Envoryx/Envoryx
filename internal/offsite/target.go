// Package offsite copies backups to storage outside the Envoryx host and fetches them
// back, for a project or for the whole instance after a disaster. A target is an
// S3-compatible bucket (AWS, Backblaze B2, Wasabi, Hetzner Object Storage, Cloudflare R2,
// MinIO), an SFTP server (Hetzner Storage Box, a NAS) or a WebDAV share (Nextcloud,
// ownCloud).
//
// The local backup stays the working copy; the target holds a copy with its own
// retention. Archives are optionally encrypted with age (scrypt passphrase) before they
// leave the host, because a backup carries the project's secrets.
package offsite

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/secrets"
	"github.com/envoryx/envoryx/internal/validate"
)

// Target types.
const (
	TypeS3     = "s3"
	TypeSFTP   = "sftp"
	TypeWebDAV = "webdav"
)

// Target is one place backups are copied to. Secret fields are never returned by the API
// (see Public); an update that leaves them empty keeps the stored value.
type Target struct {
	ID string `json:"id"`
	// Managed marks the hoster's target from the plan (ManagedID): used like the others,
	// but the instance's admins can't change or remove it.
	Managed bool   `json:"managed,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	// Auto uploads every scheduled project backup; manual ones go up on request.
	Auto bool `json:"auto"`
	// Instance takes a daily instance backup (database and configuration) at
	// InstanceHour and uploads it. That's what a fresh Envoryx needs to come back after
	// losing the host.
	Instance     bool `json:"instance"`
	InstanceHour int  `json:"instanceHour"`
	// Prefix is the directory inside the bucket or share (default "envoryx").
	Prefix string `json:"prefix"`
	// Keep is how many scheduled backups per project stay on the target (0 keeps all);
	// backups uploaded by hand are never rotated away.
	Keep int `json:"keep"`
	// InstanceKeep is the same for the daily instance backups.
	InstanceKeep int `json:"instanceKeep"`
	// Encrypt seals archives with age using Passphrase.
	Encrypt    bool   `json:"encrypt"`
	Passphrase string `json:"passphrase,omitempty"`

	// S3-compatible storage. Addressing is path-style (endpoint/bucket/key), which every
	// provider supports.
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	AccessKey string `json:"accessKey,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`

	// SFTP. HostKey pins the server's key (SHA256 fingerprints or public keys, one per
	// line); the first successful connection records its fingerprint.
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	HostKey    string `json:"hostKey,omitempty"`

	// WebDAV (User and Password are shared with SFTP).
	URL string `json:"url,omitempty"`
}

// Secrets reports which secret fields hold a value, for the UI.
type Secrets struct {
	Passphrase bool `json:"passphrase"`
	SecretKey  bool `json:"secretKey"`
	Password   bool `json:"password"`
	PrivateKey bool `json:"privateKey"`
}

// Public strips the secrets.
func (t Target) Public() Target {
	t.Passphrase, t.SecretKey, t.Password, t.PrivateKey = "", "", "", ""
	return t
}

// HasSecrets reports the stored secrets.
func (t Target) HasSecrets() Secrets {
	return Secrets{Passphrase: t.Passphrase != "", SecretKey: t.SecretKey != "", Password: t.Password != "", PrivateKey: t.PrivateKey != ""}
}

// Location describes where the target writes, for lists and logs.
func (t Target) Location() string {
	switch t.Type {
	case TypeS3:
		return strings.TrimRight(t.Endpoint, "/") + "/" + t.Bucket + "/" + t.Prefix
	case TypeSFTP:
		return fmt.Sprintf("sftp://%s@%s:%d/%s", t.User, t.Host, t.Port, t.Prefix)
	case TypeWebDAV:
		return strings.TrimRight(t.URL, "/") + "/" + t.Prefix
	}
	return ""
}

var (
	prefixRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	userRe   = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,128}$`)
)

// normalize trims, fills defaults and validates. Secrets must already be merged.
func (t *Target) normalize() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", validate.ErrInvalid, fmt.Sprintf(format, a...))
	}
	t.Name = strings.TrimSpace(t.Name)
	t.Type = strings.ToLower(strings.TrimSpace(t.Type))
	t.Prefix = strings.Trim(strings.TrimSpace(t.Prefix), "/")
	if t.Prefix == "" {
		t.Prefix = "envoryx"
	}
	if t.Name == "" || len(t.Name) > 64 {
		return bad("the target needs a name of at most 64 characters")
	}
	if !prefixRe.MatchString(t.Prefix) || strings.Contains("/"+t.Prefix+"/", "/../") || strings.Contains("/"+t.Prefix+"/", "/./") {
		return bad("the folder may contain letters, digits, dots, dashes, underscores and slashes")
	}
	if t.Keep < 0 || t.Keep > 1000 || t.InstanceKeep < 0 || t.InstanceKeep > 1000 {
		return bad("keep must be between 0 (keep all) and 1000")
	}
	if t.InstanceHour < 0 || t.InstanceHour > 23 {
		return bad("the hour must be between 0 and 23")
	}
	if t.Encrypt && len(t.Passphrase) < 12 {
		return bad("the passphrase needs at least 12 characters")
	}
	switch t.Type {
	case TypeS3:
		t.Endpoint = strings.TrimRight(strings.TrimSpace(t.Endpoint), "/")
		t.Region = strings.TrimSpace(t.Region)
		t.Bucket = strings.TrimSpace(t.Bucket)
		t.AccessKey = strings.TrimSpace(t.AccessKey)
		if t.Region == "" {
			t.Region = "us-east-1"
		}
		u, err := url.Parse(t.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.User != nil {
			return bad("the endpoint must be a URL like https://s3.eu-central-003.backblazeb2.com")
		}
		if !bucketRe.MatchString(t.Bucket) {
			return bad("invalid bucket name %q", t.Bucket)
		}
		if !regionRe.MatchString(t.Region) {
			return bad("invalid region %q", t.Region)
		}
		if t.AccessKey == "" || t.SecretKey == "" {
			return bad("S3 needs an access key and a secret key")
		}
	case TypeSFTP:
		t.Host = strings.TrimSpace(t.Host)
		t.User = strings.TrimSpace(t.User)
		t.HostKey = strings.TrimSpace(t.HostKey)
		if _, err := parseHostKey(t.HostKey); err != nil {
			return fmt.Errorf("%w: %w", validate.ErrInvalid, err)
		}
		if t.Port == 0 {
			t.Port = 22
		}
		if err := validate.Hostname(t.Host); err != nil && net.ParseIP(t.Host) == nil {
			return bad("invalid host %q", t.Host)
		}
		if t.Port < 1 || t.Port > 65535 {
			return bad("invalid port %d", t.Port)
		}
		if !userRe.MatchString(t.User) {
			return bad("invalid user name")
		}
		if t.Password == "" && t.PrivateKey == "" {
			return bad("SFTP needs a password or a private key")
		}
		if t.PrivateKey != "" {
			if _, err := parsePrivateKey(t.PrivateKey); err != nil {
				return bad("the private key cannot be read (%v); an OpenSSH key without passphrase is expected", err)
			}
		}
	case TypeWebDAV:
		t.URL = strings.TrimRight(strings.TrimSpace(t.URL), "/")
		t.User = strings.TrimSpace(t.User)
		u, err := url.Parse(t.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return bad("the WebDAV address must be a URL like https://cloud.example.com/remote.php/dav/files/me")
		}
	default:
		return bad("unknown target type %q (s3, sftp or webdav)", t.Type)
	}
	return nil
}

const configFile = "offsite.json"

// ErrNotFound is returned for unknown target ids.
var ErrNotFound = errors.New("offsite target not found")

// ErrManaged is returned for changes to the hoster's target.
var ErrManaged = fmt.Errorf("%w: the hoster's offsite target comes with the plan and can't be changed here", validate.ErrInvalid)

// ManagedID is the id of the hoster's target.
const ManagedID = "hoster"

// Config holds the targets in <config>/offsite.json (mode 0600: it carries credentials),
// and the hoster's target from the plan, which is never written there.
type Config struct {
	dir string
	mu  sync.Mutex
	// targets in the order they were added.
	targets []Target
	managed *Target
}

// SetManaged sets the hoster's target (nil for none). An SFTP host key seen before
// stays pinned while the target keeps its host.
func (c *Config) SetManaged(t *Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t != nil {
		t.ID, t.Managed = ManagedID, true
		if old := c.managed; old != nil && t.HostKey == "" && old.Host == t.Host && old.Port == t.Port {
			t.HostKey = old.HostKey
		}
	}
	c.managed = t
}

// FromPlan turns the plan's offsite target into a target that writes below a folder of
// the instance's own.
func FromPlan(p *plan.OffsiteTarget, instanceID string) (Target, error) {
	prefix := strings.Trim(strings.TrimSpace(p.Prefix), "/")
	if prefix == "" {
		prefix = "envoryx"
	}
	t := Target{ID: ManagedID, Managed: true, Name: "Hoster", Type: p.Type, Enabled: true, Auto: p.Projects, Instance: true,
		InstanceHour: p.InstanceHour, InstanceKeep: p.InstanceKeep, Keep: p.Keep, Prefix: prefix + "/" + instanceID,
		Encrypt: p.Encrypt, Passphrase: p.Passphrase, Endpoint: p.Endpoint, Region: p.Region, Bucket: p.Bucket,
		AccessKey: p.AccessKey, SecretKey: p.SecretKey, Host: p.Host, Port: p.Port, User: p.User, Password: p.Password,
		PrivateKey: p.PrivateKey, HostKey: p.HostKey, URL: p.URL}
	if err := t.normalize(); err != nil {
		return Target{}, fmt.Errorf("the hoster's offsite target: %w", err)
	}
	return t, nil
}

// OpenConfig loads the targets from dir.
func OpenConfig(dir string) (*Config, error) {
	c := &Config{dir: dir}
	raw, err := secrets.ReadFile(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read offsite configuration: %w", err)
	}
	if err := json.Unmarshal(raw, &c.targets); err != nil {
		return nil, fmt.Errorf("offsite configuration %s is unreadable: %w", filepath.Join(dir, configFile), err)
	}
	return c, nil
}

// Targets returns a copy of every target, secrets included, the hoster's first.
func (c *Config) Targets() []Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.managed != nil {
		return append([]Target{*c.managed}, c.targets...)
	}
	return slices.Clone(c.targets)
}

// Get returns one target, secrets included.
func (c *Config) Get(id string) (Target, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.managed != nil && id == ManagedID {
		return *c.managed, nil
	}
	for _, t := range c.targets {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, ErrNotFound
}

// Prepare merges a submitted target with the stored one (empty secrets keep the stored
// values) and validates it, without saving. The id is kept or generated.
func (c *Config) Prepare(t Target) (Target, error) {
	if t.ID == ManagedID {
		return Target{}, ErrManaged
	}
	t.Managed = false
	if t.ID != "" {
		old, err := c.Get(t.ID)
		if err != nil {
			return Target{}, err
		}
		if t.Passphrase == "" {
			t.Passphrase = old.Passphrase
		}
		if t.SecretKey == "" {
			t.SecretKey = old.SecretKey
		}
		if t.Password == "" {
			t.Password = old.Password
		}
		if t.PrivateKey == "" {
			t.PrivateKey = old.PrivateKey
		}
		// Another host means another key: the pin goes with it.
		if t.Type == TypeSFTP && (old.Host != strings.TrimSpace(t.Host) || old.Port != t.Port) && t.HostKey == old.HostKey {
			t.HostKey = ""
		}
	} else {
		t.ID = newID()
	}
	if err := t.normalize(); err != nil {
		return Target{}, err
	}
	return t, nil
}

// Save stores a prepared target, replacing the one with the same id.
func (c *Config) Save(t Target) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := slices.Clone(c.targets)
	i := slices.IndexFunc(next, func(x Target) bool { return x.ID == t.ID })
	if i >= 0 {
		next[i] = t
	} else {
		next = append(next, t)
	}
	if err := c.write(next); err != nil {
		return err
	}
	c.targets = next
	return nil
}

// Delete removes a target from the configuration; what it holds stays where it is.
func (c *Config) Delete(id string) error {
	if id == ManagedID {
		return ErrManaged
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.targets, func(x Target) bool { return x.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	next := slices.Delete(slices.Clone(c.targets), i, i+1)
	if err := c.write(next); err != nil {
		return err
	}
	c.targets = next
	return nil
}

// PinHostKey records the SFTP host key seen on the first connection.
func (c *Config) PinHostKey(id, fingerprint string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == ManagedID && c.managed != nil {
		// Kept in memory: the hoster's target isn't stored here.
		if c.managed.HostKey == "" {
			c.managed.HostKey = fingerprint
		}
		return nil
	}
	next := slices.Clone(c.targets)
	for i := range next {
		if next[i].ID == id && next[i].HostKey == "" {
			next[i].HostKey = fingerprint
			if err := c.write(next); err != nil {
				return err
			}
			c.targets = next
		}
	}
	return nil
}

func (c *Config) write(targets []Target) error {
	if targets == nil {
		targets = []Target{}
	}
	raw, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	return secrets.WriteFile(filepath.Join(c.dir, configFile), raw, 0o600)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
