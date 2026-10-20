package project

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// dotnet finds the solution or project to work on by itself only while the folder holds
// exactly one of them: a solution next to a project file (App.slnx beside App.csproj, the
// usual layout of a single-project repository) stops dotnet format, and dotnet build when
// the names differ, with "specify which project or solution file to use". The actions and
// the tests therefore name their target.

// dotnetTopFiles lists the solutions and project files at the top of the project.
func dotnetTopFiles(dir string) (solutions, projects []string) {
	entries, err := readDirNames(dir)
	if err != nil {
		return nil, nil
	}
	for _, name := range entries {
		switch path.Ext(name) {
		case ".sln", ".slnx":
			solutions = append(solutions, name)
		case ".csproj", ".fsproj", ".vbproj":
			projects = append(projects, name)
		}
	}
	return solutions, projects
}

// readDirNames lists the regular files of dir, sorted.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// dotnetAppProjectRe recognises an ASP.NET Core or worker project by its SDK.
var dotnetAppProjectRe = regexp.MustCompile(`Sdk="Microsoft\.NET\.Sdk\.(Web|Worker)"`)

// dotnetSolution picks the solution at the top of the project: the only one, or the .sln
// of a solution kept in both formats (dotnet sln migrate leaves the .sln behind; the .NET
// 8 SDK only reads that one). "" when there is none.
func dotnetSolution(solutions []string) (string, error) {
	switch len(solutions) {
	case 0:
		return "", nil
	case 1:
		return solutions[0], nil
	case 2:
		a, b := strings.TrimSuffix(solutions[0], path.Ext(solutions[0])), strings.TrimSuffix(solutions[1], path.Ext(solutions[1]))
		if a == b {
			for _, s := range solutions {
				if path.Ext(s) == ".sln" {
					return s, nil
				}
			}
		}
	}
	return "", fmt.Errorf("several solution files at the top of the project (%s) - keep one, or run dotnet with the one you want in the terminal", strings.Join(solutions, ", "))
}

// dotnetBuildTarget is what the dotnet build commands work on: the solution at the top of
// the project, else the one project file there.
func dotnetBuildTarget(dir string) (string, error) {
	solutions, projects := dotnetTopFiles(dir)
	sln, err := dotnetSolution(solutions)
	if err != nil || sln != "" {
		return dotnetArg(sln), err
	}
	switch len(projects) {
	case 0:
		return "", fmt.Errorf("*.sln or *.slnx or %s not found in project", strings.ReplaceAll(dotnetProjectFiles, "|", " or "))
	case 1:
		return dotnetArg(projects[0]), nil
	}
	return "", fmt.Errorf("several project files and no solution file at the top of the project - add one (dotnet new sln, dotnet sln add)")
}

// dotnetArg keeps a file name from reading as an option.
func dotnetArg(rel string) string {
	if strings.HasPrefix(rel, "-") {
		return "./" + rel
	}
	return rel
}

// dotnetProject is a project file below the project directory.
type dotnetProject struct {
	rel  string // slash-separated, relative to the project directory
	app  bool   // ASP.NET Core or worker SDK
	test bool   // a test project (dotnetTestProjectRe)
	// migrations: the project's directory holds an EF Core model snapshot.
	migrations bool
}

// dotnetProjects lists the project files up to four levels down, skipping bin/, obj/,
// node_modules/ and hidden directories like the server's project search.
func dotnetProjects(dir string) []dotnetProject {
	var projects []dotnetProject
	var snapshots []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/")
		if d.IsDir() {
			name := d.Name()
			if p != dir && (depth >= 6 || name == "bin" || name == "obj" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "ModelSnapshot.cs") {
			snapshots = append(snapshots, rel)
			return nil
		}
		switch path.Ext(d.Name()) {
		case ".csproj", ".fsproj", ".vbproj":
		default:
			return nil
		}
		if depth > 4 {
			return nil
		}
		dp := dotnetProject{rel: rel}
		if b, err := readProjectFile(dir, rel, maxProjectFile); err == nil {
			dp.app = dotnetAppProjectRe.Match(b)
			dp.test = dotnetTestProjectRe.Match(b)
		}
		projects = append(projects, dp)
		return nil
	})
	// A snapshot belongs to the project whose directory holds it most closely.
	for _, s := range snapshots {
		best, depth := -1, -1
		for i, p := range projects {
			d := path.Dir(p.rel)
			if d == "." {
				d = ""
			}
			if (d == "" || strings.HasPrefix(s, d+"/")) && len(d) > depth {
				best, depth = i, len(d)
			}
		}
		if best >= 0 {
			projects[best].migrations = true
		}
	}
	return projects
}

// dotnetEFTargets picks the projects dotnet ef works on: the startup project, which runs
// and holds the connection string, and the project with the migrations (the DbContext's
// assembly). The startup project is the one set for the server in the .NET settings, else
// the one project file at the top of the project, else the one web or worker project below
// it (the server's own choice), else the one project that is no test project. The
// migrations live in the one project that has a model snapshot, else in the startup
// project (before the first migration).
func dotnetEFTargets(dir, configured string) (project, startup string, err error) {
	projects := dotnetProjects(dir)
	var nonTest []dotnetProject
	for _, p := range projects {
		if !p.test {
			nonTest = append(nonTest, p)
		}
	}
	if len(nonTest) == 0 {
		return "", "", fmt.Errorf("%s not found in project", strings.ReplaceAll(dotnetProjectFiles, "|", " or "))
	}
	startup = configured
	if startup == "" {
		_, top := dotnetTopFiles(dir)
		if len(top) == 1 {
			startup = top[0]
		}
	}
	if startup == "" {
		if apps := filterProjects(nonTest, func(p dotnetProject) bool { return p.app }); len(apps) == 1 {
			startup = apps[0].rel
		} else if len(nonTest) == 1 {
			startup = nonTest[0].rel
		} else {
			return "", "", fmt.Errorf("found %d web or worker projects - choose the server's project in the .NET settings, or run dotnet ef with --startup-project in the terminal", len(apps))
		}
	}
	project = startup
	withMigrations := filterProjects(nonTest, func(p dotnetProject) bool { return p.migrations })
	switch {
	case len(withMigrations) == 1:
		project = withMigrations[0].rel
	case len(withMigrations) > 1 && !slices.ContainsFunc(withMigrations, func(p dotnetProject) bool { return p.rel == startup }):
		names := make([]string, len(withMigrations))
		for i, p := range withMigrations {
			names[i] = p.rel
		}
		return "", "", fmt.Errorf("several projects hold EF Core migrations (%s) - run dotnet ef with --project in the terminal", strings.Join(names, ", "))
	}
	return dotnetArg(project), dotnetArg(startup), nil
}

func filterProjects(projects []dotnetProject, keep func(dotnetProject) bool) []dotnetProject {
	var out []dotnetProject
	for _, p := range projects {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}
