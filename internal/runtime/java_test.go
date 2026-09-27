package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/validate"
)

func TestJavaConfigDefaults(t *testing.T) {
	for _, tc := range []struct {
		preset string
		port   int
		want   int
		key    string
	}{
		{"", 0, 8080, "spring-boot"},
		{"Quarkus", 0, 8080, "quarkus"},
		{"jar", 9000, 9000, "jar"}, // an explicit port wins over the preset default
	} {
		c := JavaConfig{Server: true, Preset: tc.preset, Port: tc.port}
		if err := c.Normalize(); err != nil {
			t.Fatalf("%q: %v", tc.preset, err)
		}
		if c.Preset != tc.key || c.Port != tc.want || c.Mode != JavaModeDev || c.DebugPort != 0 {
			t.Errorf("preset %q: %+v", tc.preset, c)
		}
	}
	tool := JavaConfig{Preset: "quarkus", Port: 9000, Debug: true}
	if err := tool.Normalize(); err != nil || tool != (JavaConfig{Debug: true, DebugPort: DefaultJDWPPort}) {
		t.Fatalf("a tooling container keeps only the debugger: %+v %v", tool, err)
	}
	// The jar only belongs to the "jar" preset.
	sb := JavaConfig{Server: true, Jar: "target/app.jar"}
	if err := sb.Normalize(); err != nil || sb.Jar != "" {
		t.Fatalf("spring-boot drops the jar: %+v %v", sb, err)
	}
	jar := JavaConfig{Server: true, Preset: "jar", Jar: "./build/libs/app.jar"}
	if err := jar.Normalize(); err != nil || jar.Jar != "build/libs/app.jar" {
		t.Fatalf("jar keeps a cleaned path: %+v %v", jar, err)
	}
	for _, bad := range []JavaConfig{
		{Server: true, Preset: "micronaut"},
		{Server: true, Mode: "staging"},
		{Server: true, Port: 80},
		{Server: true, Preset: "jar", Jar: "../other/app.jar"},
		{Server: true, Preset: "jar", Jar: "/opt/app.jar"},
		{Server: true, Preset: "jar", Jar: "target/app.war"},
		{Server: true, Preset: "jar", Jar: "target/$(id).jar"},
		{Server: true, Debug: true, DebugPort: 8080},
		{Debug: true, DebugPort: 70000},
	} {
		if err := bad.Normalize(); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", bad, err)
		}
	}
}

func TestJavaCommandsAndEnv(t *testing.T) {
	dev := JavaConfig{Server: true, Debug: true}
	_ = dev.Normalize()
	if cmd := dev.Command(); !slices.Equal(cmd[3:], []string{"envoryx-java", "spring-boot", "dev", "5005", ""}) {
		t.Fatalf("spring-boot dev: %q", cmd)
	}
	// The "jar" preset has no dev mode of its own: it always builds and runs the jar.
	jar := JavaConfig{Server: true, Preset: "jar", Jar: "target/app.jar"}
	_ = jar.Normalize()
	if cmd := jar.Command(); !slices.Equal(cmd[3:], []string{"envoryx-java", "jar", "production", "0", "target/app.jar"}) {
		t.Fatalf("jar: %q", cmd)
	}
	wrapped := dev.WrappedCommand("until db; do sleep 1; done")
	for _, want := range []string{"[ -e pom.xml ]", "build.gradle.kts", "until db"} {
		if !strings.Contains(wrapped[2], want) {
			t.Fatalf("guards lack %q: %s", want, wrapped[2])
		}
	}
	env := dev.Env()
	for _, want := range []string{"SERVER_PORT=8080", "SERVER_ADDRESS=0.0.0.0", "QUARKUS_HTTP_HOST=0.0.0.0", "QUARKUS_HTTP_PORT=8080", "QUARKUS_DEVSERVICES_ENABLED=false", "MICRONAUT_SERVER_PORT=8080", "PORT=8080"} {
		if !slices.Contains(env, want) {
			t.Fatalf("env lacks %s: %v", want, env)
		}
	}
}

// TestJavaServeScript runs the start script against stand-ins for mvn, gradle, the
// wrappers and java, which print what they were called with.
func TestJavaServeScript(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"mvn", "gradle", "java"} {
		stub := "#!/bin/sh\nprintf '" + name + "'; printf ' %s' \"$@\"; printf '\\n'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	project := func(files ...string) string {
		dir := t.TempDir()
		for _, f := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			content := ""
			if f == "mvnw" || f == "gradlew" {
				// Not executable on purpose: a wrapper from a zip or an upload often isn't,
				// and the script runs it through sh anyway.
				content = "#!/bin/sh\nprintf '" + f + "'; printf ' %s' \"$@\"; printf '\\n'\n"
			}
			if err := os.WriteFile(p, []byte(content), mode); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{"-c", javaServeScript, "envoryx-java"}, args...)...)
		cmd.Dir, cmd.Env = dir, []string{"PATH=" + bin + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	jdwp := "-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005"

	for _, tc := range []struct {
		name  string
		files []string
		args  []string
		want  string
	}{
		{"spring maven", []string{"pom.xml"}, []string{"spring-boot", "dev", "0", ""}, "mvn -B spring-boot:run"},
		{"spring maven debug", []string{"pom.xml"}, []string{"spring-boot", "dev", "5005", ""}, "mvn -B spring-boot:run -Dspring-boot.run.jvmArguments=" + jdwp},
		{"spring wrapper", []string{"pom.xml", "mvnw"}, []string{"spring-boot", "dev", "0", ""}, "mvnw -B spring-boot:run"},
		{"spring gradle", []string{"build.gradle.kts"}, []string{"spring-boot", "dev", "0", ""}, "gradle --no-daemon bootRun"},
		{"spring gradle debug", []string{"build.gradle", "gradlew"}, []string{"spring-boot", "dev", "5005", ""}, "gradlew --no-daemon -I " + javaGradleInit + " bootRun"},
		{"quarkus maven", []string{"pom.xml"}, []string{"quarkus", "dev", "0", ""}, "mvn -B quarkus:dev -Ddebug=false -DdebugHost=0.0.0.0"},
		{"quarkus gradle debug", []string{"build.gradle"}, []string{"quarkus", "dev", "5005", ""}, "gradle --no-daemon quarkusDev -Ddebug=5005 -DdebugHost=0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(project(tc.files...), tc.args...)
			if err != nil || out != tc.want {
				t.Fatalf("got %q (%v), want %q", out, err, tc.want)
			}
		})
	}

	t.Run("production picks the newest real jar", func(t *testing.T) {
		dir := project("pom.xml", "target/app-0.1-sources.jar", "target/app-0.1-plain.jar", "target/app-0.1.jar")
		// The real jar is the newest; the others must be skipped by name, not by age.
		later := time.Now().Add(time.Minute)
		_ = os.Chtimes(filepath.Join(dir, "target/app-0.1-sources.jar"), later, later)
		out, err := run(dir, "spring-boot", "production", "5005", "")
		want := "mvn -B -DskipTests package\njava " + jdwp + " -jar target/app-0.1.jar"
		if err != nil || out != want {
			t.Fatalf("got %q (%v), want %q", out, err, want)
		}
	})
	t.Run("quarkus production runs quarkus-run.jar", func(t *testing.T) {
		out, err := run(project("build.gradle", "build/quarkus-app/quarkus-run.jar"), "quarkus", "production", "0", "")
		if want := "gradle --no-daemon -x test build\njava -jar build/quarkus-app/quarkus-run.jar"; err != nil || out != want {
			t.Fatalf("got %q (%v)", out, err)
		}
	})
	t.Run("jar preset runs the configured jar", func(t *testing.T) {
		out, err := run(project("pom.xml", "target/worker.jar", "target/app.jar"), "jar", "production", "0", "target/worker.jar")
		if want := "mvn -B -DskipTests package\njava -jar target/worker.jar"; err != nil || out != want {
			t.Fatalf("got %q (%v)", out, err)
		}
	})
	t.Run("no jar is an error that says so", func(t *testing.T) {
		out, err := run(project("pom.xml"), "jar", "production", "0", "")
		if err == nil || !strings.Contains(out, "the build left no jar to run") {
			t.Fatalf("got %q (%v)", out, err)
		}
	})
}
