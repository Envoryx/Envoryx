package addon

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/validate"
)

//go:embed examples/*.yml
var examples embed.FS

// ErrNotFound is returned for an addon that is not installed.
var ErrNotFound = errors.New("addon not found")

// Installed is an addon file in the addons directory. A file that does not parse is
// listed with its error, so the admin sees why it is missing.
type Installed struct {
	Definition
	File  string `json:"file"`
	Error string `json:"error,omitempty"`
}

// Example is a definition shipped with Envoryx, to install as it is or to start from.
type Example struct {
	Definition
	Source string `json:"source"`
}

// Examples returns the definitions shipped with Envoryx.
func Examples() []Example {
	entries, _ := fs.ReadDir(examples, "examples")
	var out []Example
	for _, e := range entries {
		raw, err := examples.ReadFile("examples/" + e.Name())
		if err != nil {
			continue
		}
		d, err := Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("example addon %s: %v", e.Name(), err))
		}
		out = append(out, Example{Definition: d, Source: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Registry is the addons directory: one <name>.yml per addon. The file is kept as the
// admin wrote it, comments included.
type Registry struct {
	dir func() (string, error)
	mu  sync.Mutex
	// client fetches addon files from URLs; tests replace it.
	client *http.Client
}

// NewRegistry returns the registry of the directory dir returns (it may change with the
// configuration).
func NewRegistry(dir func() (string, error)) *Registry {
	return &Registry{dir: dir, client: &http.Client{Timeout: 20 * time.Second}}
}

func (r *Registry) path(name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	dir, err := r.dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yml"), nil
}

// List returns every installed addon, sorted by name.
func (r *Registry) List() ([]Installed, error) {
	dir, err := r.dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Installed{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		inst := Installed{File: e.Name()}
		if err == nil {
			inst.Definition, err = Parse(raw)
		}
		if err == nil && inst.Name+".yml" != e.Name() {
			err = fmt.Errorf("the file name must be %s.yml", inst.Name)
		}
		if err != nil {
			inst.Name, inst.Error = strings.TrimSuffix(e.Name(), ".yml"), err.Error()
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns an installed addon and its file.
func (r *Registry) Get(name string) (Definition, []byte, error) {
	p, err := r.path(name)
	if err != nil {
		return Definition{}, nil, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Definition{}, nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return Definition{}, nil, err
	}
	d, err := Parse(raw)
	return d, raw, err
}

// Save validates a definition file and stores it under its name, replacing an existing
// one.
func (r *Registry) Save(raw []byte) (Definition, error) {
	d, err := Parse(raw)
	if err != nil {
		return Definition{}, err
	}
	p, err := r.path(d.Name)
	if err != nil {
		return Definition{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Definition{}, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return Definition{}, err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return Definition{}, err
	}
	return d, nil
}

// Delete removes an addon file.
func (r *Registry) Delete(name string) error {
	p, err := r.path(name)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.Remove(p); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	} else if err != nil {
		return err
	}
	return nil
}

// Fetch downloads an addon file from an http(s) URL (a raw GitHub link, for example).
// It does not install it.
func (r *Registry) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: %q is not an http(s) URL", validate.ErrInvalid, rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: download %s: %v", validate.ErrInvalid, u.Redacted(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: download %s: HTTP %d", validate.ErrInvalid, u.Redacted(), resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: download %s: %v", validate.ErrInvalid, u.Redacted(), err)
	}
	if len(raw) > MaxSize {
		return nil, fmt.Errorf("%w: the addon file is larger than %d KB", validate.ErrInvalid, MaxSize>>10)
	}
	return raw, nil
}
