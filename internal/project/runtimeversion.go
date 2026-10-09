package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/store"
)

// A Go, Java or .NET project names the language version it is built for in its own files
// (go.mod, pom.xml or build.gradle, the .csproj and global.json). When the project's
// service runs an older version, the build or the start fails with a message from the
// tool (go.mod requires go >= …, UnsupportedClassVersionError, NETSDK1045 …) that does not
// say the fix is one select box away. The status names both versions instead, like the
// venv warning does for Python.
//
// The files are read, not evaluated: a version that comes from a variable, a parent POM,
// a Directory.Build.props or a Gradle convention plugin is not seen, and a value that is
// not a plain number is skipped. Every doubt means no warning.

// runtimeVersionWarning is the text of all three runtimes; errors.ts translates it.
func runtimeVersionWarning(file, wanted, have string) string {
	return fmt.Sprintf("%s needs %s but the project runs %s, so the build or the start fails; select a newer version in the settings or lower the requirement in the file", file, wanted, have)
}

// runtimeVersionWarnings reports project files that need a newer Go, Java or .NET than
// the project's service runs. A custom image is skipped: its version is not the
// catalogue's.
func (m *Manager) runtimeVersionWarnings(p store.Project) []string {
	var out []string
	dir := ""
	for _, kind := range []store.ServiceKind{store.ServiceGo, store.ServiceJava, store.ServiceDotnet} {
		svc := p.Service(kind)
		if svc == nil || !svc.Enabled || svc.Version == "" || svc.Custom.Set() {
			continue
		}
		if dir == "" {
			planner, err := m.planner()
			if err != nil {
				return nil
			}
			dir = planner.ProjectDir(p)
		}
		switch kind {
		case store.ServiceGo:
			out = append(out, goVersionWarnings(dir, svc.Version)...)
		case store.ServiceJava:
			out = append(out, javaVersionWarnings(dir, svc.Version)...)
		case store.ServiceDotnet:
			project := ""
			if cfg, ok := dotnetConfig(p); ok {
				project = cfg.Project
			}
			out = append(out, dotnetVersionWarnings(dir, svc.Version, project)...)
		}
	}
	return out
}

var goDirective = regexp.MustCompile(`(?m)^go[ \t]+(\d+)\.(\d+)`)

// goVersionWarnings compares the go directive of go.mod with the image's Go (major.minor
// such as "1.27"). The images set GOTOOLCHAIN=local, so a newer directive is not
// downloaded but refused. Only major.minor counts: the image carries the newest patch of
// its release, which is not known here. The toolchain line is a preference the local
// toolchain ignores, so it is not checked.
func goVersionWarnings(dir, version string) []string {
	raw, err := readProjectFile(dir, "go.mod", maxProjectFile)
	if err != nil {
		return nil
	}
	m := goDirective.FindSubmatch(raw)
	if m == nil {
		return nil
	}
	have, ok := parseMajorMinor(version)
	if !ok {
		return nil
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	if major < have[0] || (major == have[0] && minor <= have[1]) {
		return nil
	}
	return []string{runtimeVersionWarning("go.mod", fmt.Sprintf("Go %d.%d", major, minor), "Go "+version)}
}

func parseMajorMinor(v string) ([2]int, bool) {
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return [2]int{}, false
	}
	b, _, _ = strings.Cut(b, ".")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	return [2]int{x, y}, err1 == nil && err2 == nil
}

var (
	xmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// The properties a pom sets the bytecode level with; Spring Boot's parent maps
	// java.version onto maven.compiler.release.
	pomJavaVersion = regexp.MustCompile(`<(?:java\.version|maven\.compiler\.release|maven\.compiler\.source|maven\.compiler\.target)>\s*([0-9.]+)\s*</`)
	lineComment    = regexp.MustCompile(`(?m)//.*$`)
	gradleJava     = []*regexp.Regexp{
		regexp.MustCompile(`JavaLanguageVersion\.of\(\s*["']?(\d+)["']?\s*\)`),
		regexp.MustCompile(`jvmToolchain\(\s*(\d+)\s*\)`),
		regexp.MustCompile(`(?:sourceCompatibility|targetCompatibility)\s*=\s*JavaVersion\.VERSION_(\d+(?:_\d+)?)`),
		regexp.MustCompile(`(?m)(?:sourceCompatibility|targetCompatibility)\s*=\s*["']?(\d+(?:\.\d+)?)["']?\s*$`),
	}
	gradleToolchain = regexp.MustCompile(`JavaLanguageVersion|jvmToolchain`)
)

// javaVersionWarnings compares the Java level of pom.xml or build.gradle(.kts) with the
// selected JDK ("21"). Classes compiled for a newer level do not load
// (UnsupportedClassVersionError), and javac refuses a release it does not know. When a
// file names several levels the lowest counts, so a warning means every one is too new.
// A Gradle toolchain is skipped where settings.gradle provisions JDKs itself (the foojay
// resolver or toolchainManagement): Gradle downloads the one it needs.
func javaVersionWarnings(dir, version string) []string {
	have, err := strconv.Atoi(version)
	if err != nil {
		return nil
	}
	if raw, err := readProjectFile(dir, "pom.xml", maxProjectFile); err == nil {
		text := xmlComment.ReplaceAllString(string(raw), "")
		var levels []int
		for _, m := range pomJavaVersion.FindAllStringSubmatch(text, -1) {
			if n, ok := javaLevel(m[1]); ok {
				levels = append(levels, n)
			}
		}
		if lowest, ok := minInt(levels); ok && lowest > have {
			return []string{runtimeVersionWarning("pom.xml", fmt.Sprintf("Java %d", lowest), "Java "+version)}
		}
		return nil
	}
	for _, name := range []string{"build.gradle.kts", "build.gradle"} {
		raw, err := readProjectFile(dir, name, maxProjectFile)
		if err != nil {
			continue
		}
		text := lineComment.ReplaceAllString(string(raw), "")
		if gradleToolchain.MatchString(text) && gradleProvisionsJDKs(dir) {
			return nil
		}
		var levels []int
		for _, re := range gradleJava {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				if n, ok := javaLevel(strings.ReplaceAll(m[1], "_", ".")); ok {
					levels = append(levels, n)
				}
			}
		}
		if lowest, ok := minInt(levels); ok && lowest > have {
			return []string{runtimeVersionWarning(name, fmt.Sprintf("Java %d", lowest), "Java "+version)}
		}
		return nil
	}
	return nil
}

func gradleProvisionsJDKs(dir string) bool {
	for _, name := range []string{"settings.gradle.kts", "settings.gradle"} {
		raw, err := readProjectFile(dir, name, maxProjectFile)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), "foojay") || strings.Contains(string(raw), "toolchainManagement") {
			return true
		}
	}
	return false
}

// javaLevel reads "25", "1.8" (the old spelling of 8) or "17.0"; anything else is not a
// level.
func javaLevel(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "1."); ok {
		s = rest
	}
	s, _, _ = strings.Cut(s, ".")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func minInt(xs []int) (int, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	lowest := xs[0]
	for _, x := range xs[1:] {
		lowest = min(lowest, x)
	}
	return lowest, true
}

var targetFramework = regexp.MustCompile(`<TargetFramework>\s*net(\d+)\.\d+[^<]*</TargetFramework>`)

// dotnetVersionWarnings compares global.json's SDK and the project file's target
// framework with the selected SDK ("8"). An SDK builds its own framework and older ones,
// and global.json never rolls back to an older major. The project file is the configured
// one, else the one at the top of the project; a project with TargetFrameworks (several)
// or none of its own (Directory.Build.props) is skipped.
func dotnetVersionWarnings(dir, version, project string) []string {
	have, err := strconv.Atoi(version)
	if err != nil {
		return nil
	}
	var out []string
	if raw, err := readProjectFile(dir, "global.json", maxProjectFile); err == nil {
		var g struct {
			SDK struct {
				Version string `json:"version"`
			} `json:"sdk"`
		}
		if json.Unmarshal(raw, &g) == nil {
			major, _, _ := strings.Cut(g.SDK.Version, ".")
			if n, err := strconv.Atoi(major); err == nil && n > have {
				out = append(out, runtimeVersionWarning("global.json", ".NET SDK "+g.SDK.Version, ".NET "+version))
			}
		}
	}
	if project == "" {
		project = topDotnetProject(dir)
	}
	if project == "" {
		return out
	}
	raw, err := readProjectFile(dir, project, maxProjectFile)
	if err != nil {
		return out
	}
	text := xmlComment.ReplaceAllString(string(raw), "")
	m := targetFramework.FindAllStringSubmatch(text, -1)
	if len(m) != 1 {
		return out
	}
	if n, err := strconv.Atoi(m[0][1]); err == nil && n > have {
		out = append(out, runtimeVersionWarning(project, fmt.Sprintf(".NET %d", n), ".NET "+version))
	}
	return out
}

// topDotnetProject returns the one project file at the top of the project, else "".
func topDotnetProject(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".csproj", ".fsproj", ".vbproj":
			if e.Type().IsRegular() {
				if found != "" {
					return ""
				}
				found = e.Name()
			}
		}
	}
	return found
}
