package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGoVersionWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, gomod, version, want string
	}{
		{"newer minor", "module x\n\ngo 1.28\n", "1.27", "go.mod needs Go 1.28 but the project runs Go 1.27"},
		{"newer with patch", "module x\n\ngo 1.28.1\n\ntoolchain go1.28.2\n", "1.27", "needs Go 1.28 "},
		{"same minor, newer patch is not known", "module x\n\ngo 1.27.9\n", "1.27", ""},
		{"older", "module x\n\ngo 1.22\n", "1.27", ""},
		{"only the toolchain line is newer", "module x\n\ngo 1.26\n\ntoolchain go1.28.0\n", "1.27", ""},
		{"no go line", "module x\n", "1.27", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"go.mod": tc.gomod})
			got := strings.Join(goVersionWarnings(dir, tc.version), "\n")
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if w := goVersionWarnings(t.TempDir(), "1.27"); w != nil {
		t.Fatalf("no go.mod: %q", w)
	}
}

func TestJavaVersionWarnings(t *testing.T) {
	springPom := `<project><parent><artifactId>spring-boot-starter-parent</artifactId></parent>
<properties>
	<java.version>25</java.version>
</properties></project>`
	for _, tc := range []struct {
		name    string
		files   map[string]string
		version string
		want    string
	}{
		{"spring boot pom", map[string]string{"pom.xml": springPom}, "21", "pom.xml needs Java 25 but the project runs Java 21"},
		{"spring boot pom on 25", map[string]string{"pom.xml": springPom}, "25", ""},
		{"compiler release", map[string]string{"pom.xml": "<properties><maven.compiler.release>21</maven.compiler.release></properties>"}, "17", "needs Java 21"},
		{"old spelling", map[string]string{"pom.xml": "<maven.compiler.source>1.8</maven.compiler.source><maven.compiler.target>1.8</maven.compiler.target>"}, "17", ""},
		{"lowest level counts", map[string]string{"pom.xml": "<java.version>25</java.version><maven.compiler.release>17</maven.compiler.release>"}, "21", ""},
		{"commented out", map[string]string{"pom.xml": "<!-- <java.version>25</java.version> -->"}, "21", ""},
		{"property reference", map[string]string{"pom.xml": "<maven.compiler.release>${jdk}</maven.compiler.release>"}, "17", ""},
		{"gradle toolchain", map[string]string{"build.gradle": "java {\n\ttoolchain {\n\t\tlanguageVersion = JavaLanguageVersion.of(25)\n\t}\n}\n"}, "21", "build.gradle needs Java 25"},
		{"gradle kts toolchain", map[string]string{"build.gradle.kts": "java {\n    toolchain {\n        languageVersion.set(JavaLanguageVersion.of(21))\n    }\n}\n"}, "17", "build.gradle.kts needs Java 21"},
		{"gradle kotlin jvmToolchain", map[string]string{"build.gradle.kts": "kotlin {\n    jvmToolchain(25)\n}\n"}, "21", "needs Java 25"},
		{"gradle provisions its JDK", map[string]string{
			"build.gradle":    "java { toolchain { languageVersion = JavaLanguageVersion.of(25) } }\n",
			"settings.gradle": "plugins { id 'org.gradle.toolchains.foojay-resolver-convention' version '0.9.0' }\n",
		}, "21", ""},
		{"source compatibility enum", map[string]string{"build.gradle": "java {\n    sourceCompatibility = JavaVersion.VERSION_21\n}\n"}, "17", "needs Java 21"},
		{"source compatibility string", map[string]string{"build.gradle": "sourceCompatibility = '17'\n"}, "21", ""},
		{"source compatibility old enum", map[string]string{"build.gradle": "sourceCompatibility = JavaVersion.VERSION_1_8\n"}, "17", ""},
		{"gradle comment", map[string]string{"build.gradle": "// languageVersion = JavaLanguageVersion.of(25)\n"}, "21", ""},
		{"nothing to build", map[string]string{}, "21", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tc.files)
			got := strings.Join(javaVersionWarnings(dir, tc.version), "\n")
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDotnetVersionWarnings(t *testing.T) {
	csproj := func(tfm string) string {
		return `<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup>` + tfm + `</PropertyGroup></Project>`
	}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		project string
		version string
		want    []string
	}{
		{"newer framework", map[string]string{"Shop.csproj": csproj("<TargetFramework>net10.0</TargetFramework>")}, "", "8", []string{"Shop.csproj needs .NET 10 but the project runs .NET 8"}},
		{"same framework", map[string]string{"Shop.csproj": csproj("<TargetFramework>net8.0</TargetFramework>")}, "", "8", nil},
		{"older framework", map[string]string{"Shop.csproj": csproj("<TargetFramework>net8.0</TargetFramework>")}, "", "10", nil},
		{"platform suffix", map[string]string{"Shop.csproj": csproj("<TargetFramework>net10.0-windows</TargetFramework>")}, "", "8", []string{"needs .NET 10"}},
		{"several frameworks", map[string]string{"Shop.csproj": csproj("<TargetFrameworks>net8.0;net10.0</TargetFrameworks>")}, "", "8", nil},
		{"framework elsewhere", map[string]string{"Shop.csproj": csproj(""), "Directory.Build.props": "<TargetFramework>net10.0</TargetFramework>"}, "", "8", nil},
		{"two project files at the top", map[string]string{"A.csproj": csproj("<TargetFramework>net10.0</TargetFramework>"), "B.csproj": csproj("<TargetFramework>net10.0</TargetFramework>")}, "", "8", nil},
		{"configured project", map[string]string{"src/Shop/Shop.csproj": csproj("<TargetFramework>net10.0</TargetFramework>")}, "src/Shop/Shop.csproj", "8", []string{"src/Shop/Shop.csproj needs .NET 10"}},
		{"global.json", map[string]string{"global.json": `{"sdk": {"version": "10.0.100", "rollForward": "latestFeature"}}`}, "", "8", []string{"global.json needs .NET SDK 10.0.100 but the project runs .NET 8"}},
		{"global.json matches", map[string]string{"global.json": `{"sdk": {"version": "8.0.400"}}`}, "", "8", nil},
		{"global.json without sdk", map[string]string{"global.json": `{"test": {"runner": "Microsoft.Testing.Platform"}}`}, "", "8", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tc.files)
			got := dotnetVersionWarnings(dir, tc.version, tc.project)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if !strings.Contains(got[i], tc.want[i]) {
					t.Fatalf("got %q, want %q", got[i], tc.want[i])
				}
			}
		})
	}
}

// The Spring Boot template builds for Java 25; a switch to Java 21 afterwards makes the
// server crash on UnsupportedClassVersionError, and the project says why.
func TestRuntimeVersionWarningAfterJavaDowngrade(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, javaRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	dir := filepath.Join(e.projDir, v.Project.Path)
	writeFiles(t, dir, map[string]string{"pom.xml": "<project><properties><java.version>25</java.version></properties></project>"})
	find := func() string {
		t.Helper()
		got, err := e.m.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range got.Status.Warnings {
			if strings.Contains(w, "pom.xml needs") {
				return w
			}
		}
		return ""
	}
	if w := find(); w != "" {
		t.Fatalf("unexpected warning on Java 25: %q", w)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Java: &JavaUpdate{Enabled: true, Version: "21", Config: runtime.JavaConfig{Server: true, Preset: "spring-boot"}}}); err != nil {
		t.Fatal(err)
	}
	if w := find(); !strings.Contains(w, "needs Java 25 but the project runs Java 21") {
		t.Fatalf("warning = %q", w)
	}
}
