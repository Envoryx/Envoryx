package project

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A runtime service (PHP, Node, Python, Go, Ruby, Java, .NET) can run an image of the
// user's instead of the catalogue's: a registry reference, or a Dockerfile in the project
// that Envoryx builds. Workers and cron jobs run the runtime's image, so they follow.
// Databases, services and web servers keep the catalogue images.
//
// A built image is tagged envoryx-build/<kind>:<hash of the build context>, so a changed
// Dockerfile (or a file next to it) gives a new tag, which the next start builds. Projects
// and branch environments with the same context share the image.
//
// Envoryx relies on a few programs in a runtime image (a shell, git, socat and the
// runtime's own tools). An image is checked when it is set or built; what is missing is
// reported as warnings, but the image is used anyway.

// buildRepoPrefix starts the repository of every image Envoryx builds.
const buildRepoPrefix = "envoryx-build/"

// labelBuild marks the images Envoryx built; its value is the runtime.
const labelBuild = "envoryx.build"

// labelRuntime is the label Envoryx's runtime images carry (value: the runtime).
const labelRuntime = "envoryx.runtime"

// Limits of a build context: the Dockerfile's directory is sent to Docker as a whole.
const (
	maxBuildContextFiles = 5000
	maxBuildContextBytes = 512 << 20
	// maxBuildOutput is how much of a build's output is kept for the UI.
	maxBuildOutput = 32 << 10
)

// SettingRegistries is the settings key of the private registry logins.
const SettingRegistries = "registries"

// customImageKinds are the services that can run a custom image.
var customImageKinds = map[store.ServiceKind]bool{
	store.ServicePHP: true, store.ServiceNode: true, store.ServicePython: true, store.ServiceGo: true,
	store.ServiceRuby: true, store.ServiceJava: true, store.ServiceDotnet: true,
}

// SupportsCustomImage reports whether a service kind can run a custom image.
func SupportsCustomImage(kind store.ServiceKind) bool { return customImageKinds[kind] }

func isBuildRef(ref string) bool { return strings.HasPrefix(ref, buildRepoPrefix) }

// buildsDockerfile reports whether a runtime of the project builds a Dockerfile.
func buildsDockerfile(p store.Project) bool {
	for _, s := range p.Services {
		if customImageKinds[s.Kind] && s.Custom.Dockerfile != "" {
			return true
		}
	}
	return false
}

// buildSpec is how to build one envoryx-build tag.
type buildSpec struct {
	kind store.ServiceKind
	// dir is the build context (the Dockerfile's directory), dockerfile the file in it.
	dir, dockerfile string
	// rel is the Dockerfile as the project names it, for messages.
	rel string
	// err is why the context could not be read; building fails with it.
	err error
}

// customImages holds the build specs of the tags resolveImages handed out and the file
// hashes of the build contexts (keyed by path, size and modification time).
type customImages struct {
	specs  sync.Map // tag -> buildSpec
	hashes sync.Map // hashKey -> string
	builds sync.Map // tag -> *sync.Mutex
}

type hashKey struct {
	path string
	size int64
	mod  time.Time
}

// catalogKey maps a service to its catalogue entry ("" for none).
func catalogKey(svc store.ProjectService) string {
	switch svc.Kind {
	case store.ServicePHP, store.ServiceNode, store.ServicePython, store.ServiceGo, store.ServiceRuby, store.ServiceJava, store.ServiceDotnet:
		return string(svc.Kind)
	case store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama:
		return string(svc.Kind)
	case store.ServiceWeb, store.ServiceDatabase, store.ServiceStorage:
		return svc.Variant
	}
	return ""
}

// catalogImage returns the catalogue image of a service in place of a custom one. One-shot
// helpers (git, templates) use it: they run before a Dockerfile in the repository exists
// and need the tools only Envoryx's images promise.
func (m *Manager) catalogImage(svc store.ProjectService) string {
	if !svc.Custom.Set() {
		return svc.Image
	}
	if key := catalogKey(svc); key != "" {
		if v, err := m.catalog.Resolve(key, svc.Version); err == nil && v.Image != "" {
			return v.Image
		}
	}
	return svc.Image
}

// resolveCustomImage points a runtime service at its custom image: the reference itself,
// or the build tag of its Dockerfile's current context.
func (m *Manager) resolveCustomImage(p store.Project, svc *store.ProjectService) {
	if !customImageKinds[svc.Kind] || !svc.Custom.Set() {
		return
	}
	if svc.Custom.Image != "" {
		svc.Image = svc.Custom.Image
		return
	}
	spec := buildSpec{kind: svc.Kind, rel: svc.Custom.Dockerfile}
	tag := ""
	dir, err := m.dockerfileDir(p, svc.Custom.Dockerfile)
	if err == nil {
		spec.dir, spec.dockerfile = dir, path.Base(svc.Custom.Dockerfile)
		var sum string
		sum, err = m.contextHash(dir, spec.dockerfile)
		if err == nil {
			tag = buildRepoPrefix + string(svc.Kind) + ":" + sum[:16]
		}
	}
	if err != nil {
		spec.err = err
		// The tag never exists, so ensureImage reports the error when the image is needed.
		tag = buildRepoPrefix + string(svc.Kind) + ":unavailable"
	}
	m.custom.specs.Store(tag, spec)
	svc.Image = tag
}

// dockerfileDir returns the directory of a project's Dockerfile inside the Envoryx
// container.
func (m *Manager) dockerfileDir(p store.Project, dockerfile string) (string, error) {
	paths, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	dir, err := validate.ResolveUnder(paths.ProjectsDir, p.Path)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(path.Dir(dockerfile))), nil
}

// validateDockerfilePath checks a Dockerfile path relative to the project directory.
func validateDockerfilePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || !fs.ValidPath(p) || path.Clean(p) != p {
		return fmt.Errorf("%w: the Dockerfile must be a path inside the project, like .envoryx/php.Dockerfile", validate.ErrInvalid)
	}
	return nil
}

// validateImageRef checks a registry image reference.
func validateImageRef(ref string) error {
	if ref == "" || strings.ContainsAny(ref, " \t\n") || docker.RegistryHost(ref) == "" {
		return fmt.Errorf("%w: %q is not an image reference (like ghcr.io/acme/php:8.4)", validate.ErrInvalid, ref)
	}
	if isBuildRef(ref) || isRollbackRef(ref) {
		return fmt.Errorf("%w: %s images are Envoryx's own", validate.ErrInvalid, ref[:strings.Index(ref, "/")])
	}
	return nil
}

// contextFile is one file of a build context.
type contextFile struct {
	rel  string
	info fs.FileInfo
}

// walkContext lists the regular files and directories of a build context. It goes through
// an os.Root, so a symlink cannot reach outside the context; symlinks are left out.
func walkContext(dir string) (*os.Root, []contextFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open the build context: %w", err)
	}
	var files []contextFile
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, contextFile{rel: rel, info: info})
		total += info.Size()
		if len(files) > maxBuildContextFiles || total > maxBuildContextBytes {
			return fmt.Errorf("the build context %s is too large (more than %d files or %d MB): put the Dockerfile in a directory of its own, like .envoryx/", dir, maxBuildContextFiles, maxBuildContextBytes>>20)
		}
		return nil
	})
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return root, files, nil
}

// contextHash hashes the Dockerfile's name and every file of the context.
func (m *Manager) contextHash(dir, dockerfile string) (string, error) {
	root, files, err := walkContext(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	found := false
	h := sha256.New()
	fmt.Fprintf(h, "dockerfile %s\n", dockerfile)
	for _, f := range files {
		if f.rel == dockerfile {
			found = true
		}
		if f.info.IsDir() {
			fmt.Fprintf(h, "dir %s %o\n", f.rel, f.info.Mode().Perm())
			continue
		}
		sum, err := m.fileHash(root, filepath.Join(dir, f.rel), f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "file %s %o %s\n", f.rel, f.info.Mode().Perm(), sum)
	}
	if !found {
		return "", fmt.Errorf("the Dockerfile %s does not exist", filepath.Join(dir, dockerfile))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileHash returns the SHA-256 of a file, cached while its size and time stay the same.
func (m *Manager) fileHash(root *os.Root, abs string, f contextFile) (string, error) {
	key := hashKey{path: abs, size: f.info.Size(), mod: f.info.ModTime()}
	if v, ok := m.custom.hashes.Load(key); ok {
		return v.(string), nil
	}
	r, err := root.Open(f.rel)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	m.custom.hashes.Store(key, sum)
	return sum, nil
}

// contextTar streams the context as a tar archive (the build's input).
func contextTar(dir string) (io.ReadCloser, error) {
	root, files, err := walkContext(dir)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer root.Close()
		tw := tar.NewWriter(pw)
		err := func() error {
			for _, f := range files {
				hdr, err := tar.FileInfoHeader(f.info, "")
				if err != nil {
					return err
				}
				hdr.Name = filepath.ToSlash(f.rel)
				hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
				if f.info.IsDir() {
					hdr.Name += "/"
				}
				if err := tw.WriteHeader(hdr); err != nil {
					return err
				}
				if f.info.IsDir() {
					continue
				}
				r, err := root.Open(f.rel)
				if err != nil {
					return err
				}
				_, err = io.CopyN(tw, r, f.info.Size())
				_ = r.Close()
				if err != nil {
					return err
				}
			}
			return tw.Close()
		}()
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// ensureImage makes an image available: a build tag is built when missing, anything else
// is pulled when missing. Of proj only the ID and slug are used.
func (m *Manager) ensureImage(ctx context.Context, proj store.Project, ref string) error {
	if !isBuildRef(ref) {
		return m.engine.EnsureImage(ctx, ref, m.pullProgress(ctx, proj.Slug, ref))
	}
	exists, err := m.engine.ImageExists(ctx, ref)
	if err != nil || exists {
		return err
	}
	return m.buildImage(ctx, proj, ref, false)
}

// buildImage builds a build tag from its spec and records the output (and, after a
// success, the check of the new image) on the project's service. fresh pulls the base
// images again and ignores the build cache.
func (m *Manager) buildImage(ctx context.Context, proj store.Project, ref string, fresh bool) error {
	v, ok := m.custom.specs.Load(ref)
	if !ok {
		return fmt.Errorf("no Dockerfile is known for %s", ref)
	}
	spec := v.(buildSpec)
	if spec.err != nil {
		m.recordBuild(ctx, proj, spec.kind, ref, spec.err.Error(), true)
		return fmt.Errorf("build the %s image from %s: %w", spec.kind, spec.rel, spec.err)
	}
	mu, _ := m.custom.builds.LoadOrStore(ref, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if !fresh {
		// Another project may have built the same context meanwhile.
		if exists, err := m.engine.ImageExists(ctx, ref); err == nil && exists {
			return nil
		}
	}
	tarball, err := contextTar(spec.dir)
	if err != nil {
		m.recordBuild(ctx, proj, spec.kind, ref, err.Error(), true)
		return err
	}
	defer tarball.Close()
	out := &tailBuffer{limit: maxBuildOutput}
	last := time.Time{}
	step(ctx, "Building the image {{image}} from {{dockerfile}}", "image", ref, "dockerfile", spec.rel)
	err = m.engine.BuildImage(ctx, docker.BuildOptions{
		Context:    tarball,
		Dockerfile: spec.dockerfile,
		Tag:        ref,
		// Not envoryx.managed: containers inherit image labels, and a foreign container from
		// this image must not look like Envoryx's.
		Labels:  map[string]string{labelBuild: string(spec.kind)},
		Pull:    fresh,
		NoCache: fresh,
		Output: func(line string) {
			_, _ = out.Write([]byte(line + "\n"))
			// Docker prints every step; a line a second is enough for the progress text.
			if strings.HasPrefix(line, "Step ") || time.Since(last) > time.Second {
				last = time.Now()
				step(ctx, "Building the image {{image}}: {{status}}", "image", ref, "status", line)
			}
		},
	})
	m.recordBuild(ctx, proj, spec.kind, ref, strings.TrimRight(out.String(), "\n"), err != nil)
	if err != nil {
		m.log.Warn("image build failed", "project", proj.Slug, "image", ref, "err", err)
		return fmt.Errorf("build the %s image from %s: %w", spec.kind, spec.rel, err)
	}
	m.log.Info("image built", "project", proj.Slug, "image", ref)
	m.checkCustomImage(ctx, proj.ID, spec.kind, ref)
	return nil
}

// recordBuild stores a build's output on the service, if it still builds that Dockerfile.
func (m *Manager) recordBuild(ctx context.Context, proj store.Project, kind store.ServiceKind, ref, output string, failed bool) {
	ctx = context.WithoutCancel(ctx)
	cur, err := m.store.Projects.Get(ctx, proj.ID)
	if err != nil {
		return
	}
	svc := cur.Service(kind)
	if svc == nil || svc.Custom.Dockerfile == "" {
		return
	}
	c := svc.Custom
	c.BuildOutput, c.BuildFailed = output, failed
	if failed {
		c.Warnings, c.CheckedImage, c.CheckedAt = nil, "", time.Time{}
	}
	if err := m.store.Projects.SetCustomImage(ctx, proj.ID, kind, c); err != nil {
		m.log.Warn("recording the build failed", "project", proj.Slug, "err", err)
	}
}

// ---- the check -----------------------------------------------------------------------

// probeTool is a program the check looks for: a required one, or an optional one whose
// feature needs it.
type probeTool struct {
	bin string
	// alt is another name that counts too (python for python3).
	alt string
	// feature is what does not work without it; empty for required programs.
	feature string
}

var probeCommon = []probeTool{
	{bin: "sh"},
	{bin: "git", feature: "git in the terminal and packages installed from git repositories"},
	{bin: "socat", feature: "waiting for the database at start and SSH port forwarding"},
	{bin: "ssh", feature: "git over SSH (deploy keys)"},
}

var probeTools = map[store.ServiceKind][]probeTool{
	store.ServicePHP:    {{bin: "php"}, {bin: "php-fpm"}, {bin: "composer", feature: "Composer installs"}},
	store.ServiceNode:   {{bin: "node"}, {bin: "npm"}, {bin: "corepack", feature: "pnpm and Yarn"}},
	store.ServicePython: {{bin: "python3", alt: "python"}, {bin: "pip", alt: "pip3", feature: "pip installs"}, {bin: "uv", feature: "uv projects"}},
	store.ServiceGo:     {{bin: "go"}, {bin: "air", feature: "live reload"}, {bin: "dlv", feature: "debugging"}, {bin: "gotestsum", feature: "test reports"}},
	store.ServiceRuby:   {{bin: "ruby"}, {bin: "bundle"}, {bin: "rdbg", feature: "debugging"}},
	store.ServiceJava:   {{bin: "java"}, {bin: "mvn", feature: "Maven projects without a wrapper"}, {bin: "gradle", feature: "Gradle projects without a wrapper"}},
	store.ServiceDotnet: {{bin: "dotnet"}, {bin: "netcoredbg", feature: "debugging"}, {bin: "dotnet-ef", feature: "EF Core migrations"}},
}

// knownEntrypoints run their arguments; any other ENTRYPOINT may not.
var knownEntrypoints = map[string]bool{"tini": true, "docker-php-entrypoint": true, "docker-entrypoint.sh": true, "dumb-init": true}

// probeImage checks an image for what Envoryx needs and returns the warnings.
func (m *Manager) probeImage(ctx context.Context, projectID, slug string, kind store.ServiceKind, ref string) []string {
	var warnings []string
	info, err := m.engine.InspectImage(ctx, ref)
	if err != nil {
		return []string{fmt.Sprintf("the image could not be inspected: %v", err)}
	}
	if rt := info.Labels[labelRuntime]; rt != "" && rt != string(kind) {
		warnings = append(warnings, fmt.Sprintf("the image is based on Envoryx's %s image, not the %s one", rt, kind))
	}
	if len(info.Entrypoint) > 0 && !knownEntrypoints[path.Base(info.Entrypoint[0])] {
		warnings = append(warnings, fmt.Sprintf("the image's ENTRYPOINT %s runs before every command Envoryx starts; it must end with exec \"$@\"", strings.Join(info.Entrypoint, " ")))
	}
	tools := append(append([]probeTool{}, probeCommon...), probeTools[kind]...)
	var script strings.Builder
	for _, t := range tools {
		for _, b := range []string{t.bin, t.alt} {
			if b != "" {
				fmt.Fprintf(&script, "command -v %s >/dev/null 2>&1 && echo %s; ", b, b)
			}
		}
	}
	if kind == store.ServicePHP {
		// Installed is enough: Envoryx loads Xdebug only when it is switched on.
		script.WriteString(`{ php -m 2>/dev/null | grep -qix xdebug || test -f "$(php -r 'echo ini_get("extension_dir");' 2>/dev/null)/xdebug.so"; } && echo ext:xdebug; `)
	}
	script.WriteString("true")
	res, err := m.engine.RunOneShot(ctx, docker.ContainerSpec{
		Name:          fmt.Sprintf("envoryx-%s-imagecheck-%d", slug, time.Now().UnixNano()%1_000_000),
		Image:         ref,
		Labels:        docker.ManagedLabels(projectID, slug, "imagecheck", ""),
		Entrypoint:    []string{"sh", "-c"},
		Cmd:           []string{script.String()},
		RestartPolicy: "no",
	})
	if err != nil || res.ExitCode != 0 {
		detail := ""
		if err != nil {
			detail = ": " + err.Error()
		} else if s := strings.TrimSpace(res.Stderr); s != "" {
			detail = ": " + s
		}
		return append(warnings, "the image has no working /bin/sh, which Envoryx runs every command with"+detail)
	}
	found := map[string]bool{}
	for _, l := range strings.Fields(res.Stdout) {
		found[l] = true
	}
	for _, t := range tools {
		if found[t.bin] || (t.alt != "" && found[t.alt]) {
			continue
		}
		if t.feature == "" {
			warnings = append(warnings, fmt.Sprintf("%s is missing; Envoryx needs it", t.bin))
		} else {
			warnings = append(warnings, fmt.Sprintf("%s is missing: no %s", t.bin, t.feature))
		}
	}
	if kind == store.ServicePHP && found["php"] && !found["ext:xdebug"] {
		warnings = append(warnings, "the Xdebug extension is missing: no step debugging")
	}
	if info.Labels[labelRuntime] == "" && kind == store.ServicePHP {
		warnings = append(warnings, "the image is not based on Envoryx's PHP image: the PHP extension switches have no effect")
	}
	return warnings
}

// checkCustomImage probes the service's image and stores the warnings.
func (m *Manager) checkCustomImage(ctx context.Context, projectID string, kind store.ServiceKind, ref string) {
	ctx = context.WithoutCancel(ctx)
	proj, err := m.store.Projects.Get(ctx, projectID)
	if err != nil {
		return
	}
	step(ctx, "Checking the image {{image}}", "image", ref)
	warnings := m.probeImage(ctx, proj.ID, proj.Slug, kind, ref)
	svc := proj.Service(kind)
	if svc == nil || !svc.Custom.Set() {
		return
	}
	c := svc.Custom
	c.Warnings, c.CheckedImage, c.CheckedAt = warnings, ref, time.Now().UTC()
	if err := m.store.Projects.SetCustomImage(ctx, proj.ID, kind, c); err != nil {
		m.log.Warn("storing the image check failed", "project", proj.Slug, "err", err)
	}
}

// ---- setting and rebuilding --------------------------------------------------------------

// CustomImageRequest sets a service's custom image: Image or Dockerfile, both empty for
// the catalogue image.
type CustomImageRequest struct {
	Image      string `json:"image"`
	Dockerfile string `json:"dockerfile"`
}

// SetCustomImage gives a runtime service a custom image (or returns it to the catalogue
// image), makes the image available, checks it and restarts a running project with it.
// A failed pull or build is returned, but the setting stays, so the Dockerfile can be
// fixed and the image rebuilt.
func (m *Manager) SetCustomImage(ctx context.Context, id string, kind store.ServiceKind, req CustomImageRequest) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	req.Image, req.Dockerfile = strings.TrimSpace(req.Image), strings.TrimSpace(req.Dockerfile)
	if !customImageKinds[kind] {
		return View{}, fmt.Errorf("%w: only runtime services (PHP, Node.js, Python, Go, Ruby, Java, .NET) can run a custom image", validate.ErrInvalid)
	}
	switch {
	case req.Image != "" && req.Dockerfile != "":
		return View{}, fmt.Errorf("%w: give an image or a Dockerfile, not both", validate.ErrInvalid)
	case req.Image != "":
		if err := validateImageRef(req.Image); err != nil {
			return View{}, err
		}
	case req.Dockerfile != "":
		if err := validateDockerfilePath(req.Dockerfile); err != nil {
			return View{}, err
		}
	}
	return m.runView(ctx, limitProvision, Operation{Action: "image", ProjectID: id}, func(ctx context.Context) (View, error) {
		return m.customImageLocked(ctx, id, kind, func(svc *store.ProjectService) (bool, error) {
			if svc.Custom.Image == req.Image && svc.Custom.Dockerfile == req.Dockerfile {
				return false, nil
			}
			svc.Custom = store.CustomImage{Image: req.Image, Dockerfile: req.Dockerfile}
			return true, m.store.Projects.SetCustomImage(ctx, id, kind, svc.Custom)
		}, false)
	})
}

// RebuildCustomImage builds a service's Dockerfile again, with fresh base images and
// without the build cache, and restarts a running project with the result.
func (m *Manager) RebuildCustomImage(ctx context.Context, id string, kind store.ServiceKind) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	return m.runView(ctx, limitProvision, Operation{Action: "image", ProjectID: id}, func(ctx context.Context) (View, error) {
		return m.customImageLocked(ctx, id, kind, func(svc *store.ProjectService) (bool, error) {
			if svc.Custom.Dockerfile == "" {
				return false, fmt.Errorf("%w: the %s service does not build a Dockerfile", validate.ErrInvalid, kind)
			}
			return true, nil
		}, true)
	})
}

// customImageLocked applies change to the service under the project lock, then makes the
// image available (a fresh build if rebuild), checks it and restarts a running project.
func (m *Manager) customImageLocked(ctx context.Context, id string, kind store.ServiceKind, change func(*store.ProjectService) (bool, error), rebuild bool) (View, error) {
	unlock, err := m.lock(id)
	if err != nil {
		return View{}, err
	}
	defer unlock()
	proj, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	svc := proj.Service(kind)
	if svc == nil {
		return View{}, fmt.Errorf("%w: the project has no %s service", ErrNotFound, kind)
	}
	before := svc.Custom
	changed, err := change(svc)
	if err != nil {
		return View{}, err
	}
	if !changed {
		return m.Get(context.WithoutCancel(ctx), id)
	}
	m.audit.Log(ctx, audit.ActionCustomImageChanged, "project", id, map[string]any{
		"name": proj.Name, "service": string(kind), "image": svc.Custom.Image, "dockerfile": svc.Custom.Dockerfile,
		"previousImage": before.Image, "previousDockerfile": before.Dockerfile, "rebuild": rebuild,
	})
	if proj, err = m.loadProject(ctx, id); err != nil {
		return View{}, err
	}
	svc = proj.Service(kind)
	ref := svc.Image
	if svc.Custom.Set() {
		if rebuild {
			err = m.buildImage(ctx, proj, ref, true)
		} else if isBuildRef(ref) {
			err = m.ensureImage(ctx, proj, ref)
		} else {
			// A reference may name a tag that moved: fetch the current image.
			err = m.engine.PullImage(ctx, ref, m.pullProgress(ctx, proj.Slug, ref))
			if err != nil {
				if exists, _ := m.engine.ImageExists(ctx, ref); exists {
					m.log.Warn("image refresh failed, using the local image", "image", ref, "err", err)
					err = nil
				}
			}
			if err == nil {
				m.checkCustomImage(ctx, id, kind, ref)
			}
		}
		if err == nil && isBuildRef(ref) && !rebuild {
			if cur, gerr := m.store.Projects.Get(ctx, id); gerr == nil {
				if s := cur.Service(kind); s != nil && s.Custom.CheckedImage != ref {
					m.checkCustomImage(ctx, id, kind, ref)
				}
			}
		}
		if err != nil {
			return View{}, imageError(ref, err)
		}
	}
	if proj.DesiredState == store.DesiredRunning && proj.Lifecycle == store.LifecycleReady {
		planner, err := m.planner()
		if err != nil {
			return View{}, err
		}
		plan, err := planner.Plan(proj)
		if err != nil {
			return View{}, err
		}
		if err := m.stopPlan(ctx, proj, plan); err != nil {
			return View{}, err
		}
		if err := m.startPlan(ctx, proj, plan); err != nil {
			err = opError(ctx, err)
			_ = m.store.Projects.UpdateState(context.WithoutCancel(ctx), id, proj.DesiredState, proj.Lifecycle, err.Error())
			return View{}, err
		}
	}
	return m.Get(context.WithoutCancel(ctx), id)
}

// imageError makes a failed pull or build of the user's image a readable input error
// (Docker being down stays what it is).
func imageError(ref string, err error) error {
	if errors.Is(err, docker.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, validate.ErrInvalid) {
		return err
	}
	hint := ""
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "authoriz") || strings.Contains(msg, "denied") {
		hint = "; if the registry is private, add a login under Settings, Tools, Private registries"
	}
	if isBuildRef(ref) {
		return fmt.Errorf("%w: %v%s", validate.ErrInvalid, err, hint)
	}
	return fmt.Errorf("%w: the image %s could not be pulled%s (%v)", validate.ErrInvalid, ref, hint, err)
}

// ---- registry logins -------------------------------------------------------------------

// Registry is a login for a private registry. The password is write-only: Registries
// returns it empty with HasPassword set.
type Registry struct {
	Host        string `json:"host"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

func (m *Manager) readRegistries(ctx context.Context) ([]Registry, error) {
	raw, err := m.store.Settings.Get(ctx, SettingRegistries)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	out := []Registry{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("read the registry logins: %w", err)
		}
	}
	return out, nil
}

// Registries returns the registry logins without their passwords.
func (m *Manager) Registries(ctx context.Context) ([]Registry, error) {
	regs, err := m.readRegistries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range regs {
		regs[i].HasPassword, regs[i].Password = regs[i].Password != "", ""
	}
	return regs, nil
}

// normalizeRegistryHost turns what a user types (a URL, Docker Hub's names) into a host.
func normalizeRegistryHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	switch h {
	case "index.docker.io", "registry-1.docker.io", "hub.docker.com":
		return "docker.io"
	}
	return h
}

// SetRegistries replaces the registry logins. An entry without a password keeps the
// stored password of its host.
func (m *Manager) SetRegistries(ctx context.Context, regs []Registry) ([]Registry, error) {
	old, err := m.readRegistries(ctx)
	if err != nil {
		return nil, err
	}
	oldPw := map[string]string{}
	for _, r := range old {
		oldPw[r.Host] = r.Password
	}
	seen := map[string]bool{}
	out := make([]Registry, 0, len(regs))
	for _, r := range regs {
		r.Host, r.Username = normalizeRegistryHost(r.Host), strings.TrimSpace(r.Username)
		if r.Host == "" || strings.ContainsAny(r.Host, " \t@") || docker.RegistryHost(r.Host+"/x") != r.Host {
			return nil, fmt.Errorf("%w: %q is not a registry host (like ghcr.io or registry.example.com:5000)", validate.ErrInvalid, r.Host)
		}
		if seen[r.Host] {
			return nil, fmt.Errorf("%w: %s is listed twice", validate.ErrInvalid, r.Host)
		}
		seen[r.Host] = true
		if r.Username == "" {
			return nil, fmt.Errorf("%w: the login for %s needs a username", validate.ErrInvalid, r.Host)
		}
		if r.Password == "" {
			r.Password = oldPw[r.Host]
		}
		if r.Password == "" {
			return nil, fmt.Errorf("%w: the login for %s needs a password or token", validate.ErrInvalid, r.Host)
		}
		r.HasPassword = false
		out = append(out, r)
	}
	raw, _ := json.Marshal(out)
	if err := m.store.Settings.Set(ctx, SettingRegistries, string(raw)); err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(out))
	for _, r := range out {
		hosts = append(hosts, r.Host)
	}
	m.audit.Log(ctx, audit.ActionRegistriesChanged, "settings", "", map[string]any{"hosts": hosts})
	return m.Registries(ctx)
}

// registryCredentials is the engine's lookup of the registry logins.
func (m *Manager) registryCredentials() []docker.RegistryCredential {
	regs, err := m.readRegistries(context.Background())
	if err != nil {
		m.log.Warn("reading the registry logins failed", "err", err)
		return nil
	}
	out := make([]docker.RegistryCredential, 0, len(regs))
	for _, r := range regs {
		out = append(out, docker.RegistryCredential{Host: r.Host, Username: r.Username, Password: r.Password})
	}
	return out
}
