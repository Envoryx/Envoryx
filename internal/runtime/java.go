package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/validate"
)

// JavaConfig configures the Java service. Without Server the container idles as a
// tooling container (mvn, gradle, java, jshell); with it the container runs the preset's
// application server as its main process. A project without PHP is then served by it:
// the embedded proxy routes the project's primary hostname to the Java container and the
// web container's port stays unpublished.
type JavaConfig struct {
	Server bool `json:"server"`
	// Mode is "dev" (the framework's dev mode, default) or "production": the project is
	// built once and the jar runs.
	Mode string `json:"mode,omitempty"`
	// Preset selects the server command: "spring-boot" (spring-boot:run / bootRun in
	// dev), "quarkus" (quarkus:dev / quarkusDev in dev) or "jar" (build, then java -jar;
	// Micronaut, Javalin, Helidon …).
	Preset string `json:"preset,omitempty"`
	// Jar is the jar the "jar" preset runs, relative to the project. Empty means the
	// newest jar the build left in target/ or build/libs/.
	Jar string `json:"jar,omitempty"`
	// Port the server listens on inside the container (default 8080).
	Port int `json:"port,omitempty"`
	// HostPort publishes the server on the Docker host (assigned by Envoryx).
	HostPort int `json:"hostPort,omitempty"`
	// Debug starts the server's JVM with a JDWP agent that IntelliJ ("Remote JVM Debug")
	// and VS Code attach to; without Server only the port is published, for a JVM started
	// from the terminal.
	Debug bool `json:"debug,omitempty"`
	// DebugPort is the JDWP port inside the container (default 5005).
	DebugPort int `json:"debugPort,omitempty"`
	// DebugHostPort publishes the JDWP port on the Docker host (assigned by Envoryx).
	DebugHostPort int `json:"debugHostPort,omitempty"`
}

// Java run modes and defaults.
const (
	JavaModeDev        = "dev"
	JavaModeProduction = "production"
	DefaultJavaPort    = 8080
	// DefaultJDWPPort is the port IntelliJ's "Remote JVM Debug" configuration starts
	// with.
	DefaultJDWPPort = 5005
	// javaGradleInit is the Gradle init script in the image that hands ENVORYX_JDWP to
	// bootRun: Spring Boot's Gradle plugin has no property for extra JVM arguments.
	javaGradleInit = "/opt/envoryx/jdwp.gradle"
)

// JavaPreset describes a supported application server and its defaults.
type JavaPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// JavaPresets lists the supported presets in UI order.
var JavaPresets = []JavaPreset{
	{Key: "spring-boot", Label: "Spring Boot", Port: 8080},
	{Key: "quarkus", Label: "Quarkus", Port: 8080},
	{Key: "jar", Label: "Other (build, then java -jar: Micronaut, Javalin, Helidon …)", Port: 8080},
}

// JavaPresetByKey looks up a preset.
func JavaPresetByKey(key string) (JavaPreset, bool) {
	for _, p := range JavaPresets {
		if p.Key == key {
			return p, true
		}
	}
	return JavaPreset{}, false
}

// javaJarRe accepts a relative path to a jar; nothing else, so the value can go into
// the start script as an argument without surprises.
var javaJarRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*\.jar$`)

// ValidJavaJar reports whether s is a jar path in javaJarRe's form.
func ValidJavaJar(s string) bool {
	return len(s) <= 200 && javaJarRe.MatchString(s) && !strings.Contains(s, "..")
}

// Normalize validates the configuration and fills defaults.
func (c *JavaConfig) Normalize() error {
	if !c.Server {
		// A tooling container: the JDWP port stays on its own for a JVM started from the
		// terminal (a test run, a main class). Everything else is server configuration.
		*c = JavaConfig{Debug: c.Debug, DebugPort: c.DebugPort, DebugHostPort: c.DebugHostPort}
		return c.normalizeDebug()
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case "", JavaModeDev:
		c.Mode = JavaModeDev
	case JavaModeProduction:
	default:
		return fmt.Errorf("%w: mode must be dev or production", validate.ErrInvalid)
	}
	c.Preset = strings.ToLower(strings.TrimSpace(c.Preset))
	if c.Preset == "" {
		c.Preset = "spring-boot"
	}
	preset, ok := JavaPresetByKey(c.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown Java server preset %q", validate.ErrInvalid, c.Preset)
	}
	c.Jar = strings.TrimPrefix(strings.TrimSpace(c.Jar), "./")
	if c.Preset != "jar" {
		c.Jar = ""
	}
	if c.Jar != "" && !ValidJavaJar(c.Jar) {
		return fmt.Errorf("%w: invalid jar %q (use a relative path such as target/app.jar)", validate.ErrInvalid, c.Jar)
	}
	if c.Port == 0 {
		c.Port = preset.Port
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("%w: server port must be between 1024 and 65535", validate.ErrInvalid)
	}
	return c.normalizeDebug()
}

// normalizeDebug validates the JDWP port, which is published with or without the
// application server.
func (c *JavaConfig) normalizeDebug() error {
	if !c.Debug {
		c.DebugPort, c.DebugHostPort = 0, 0
		return nil
	}
	if c.DebugPort == 0 {
		c.DebugPort = DefaultJDWPPort
	}
	if c.DebugPort < 1024 || c.DebugPort > 65535 {
		return fmt.Errorf("%w: JDWP port must be between 1024 and 65535", validate.ErrInvalid)
	}
	if c.Server && c.DebugPort == c.Port {
		return fmt.Errorf("%w: JDWP port must differ from the server port", validate.ErrInvalid)
	}
	return nil
}

// Production reports whether the project is built once and the jar runs.
func (c JavaConfig) Production() bool { return c.Mode == JavaModeProduction }

// javaServeScript starts the server: $1 is the preset, $2 the mode, $3 the JDWP port (0
// when off) and $4 the jar of the "jar" preset (may be empty). The build tool follows
// the project: pom.xml means Maven, else Gradle, and a wrapper (mvnw, gradlew) wins over
// the image's tools. Wrappers run through sh, so one that lost its executable bit (a zip
// download, an upload from Windows) still works. Dev mode runs the framework's own dev goal; production and the
// "jar" preset build once and exec java, so the JVM is the container's main process and
// gets the stop signal directly.
const javaServeScript = `set -e
preset=$1 mode=$2 dport=$3 jar=$4
mvn=mvn; [ -f ./mvnw ] && mvn="sh ./mvnw"
gradle=gradle; [ -f ./gradlew ] && gradle="sh ./gradlew"
tool=gradle; [ -f pom.xml ] && tool=maven
jdwp=
[ "$dport" != 0 ] && jdwp="-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:$dport"
if [ "$mode" = dev ]; then
  case $preset in
  spring-boot)
    if [ $tool = maven ]; then
      if [ -n "$jdwp" ]; then exec $mvn -B spring-boot:run "-Dspring-boot.run.jvmArguments=$jdwp"; fi
      exec $mvn -B spring-boot:run
    fi
    if [ -n "$jdwp" ]; then export ENVORYX_JDWP="$jdwp"; exec $gradle --no-daemon -I ` + javaGradleInit + ` bootRun; fi
    exec $gradle --no-daemon bootRun ;;
  quarkus)
    debug=false; [ -n "$jdwp" ] && debug=$dport
    if [ $tool = maven ]; then exec $mvn -B quarkus:dev -Ddebug=$debug -DdebugHost=0.0.0.0; fi
    exec $gradle --no-daemon quarkusDev -Ddebug=$debug -DdebugHost=0.0.0.0 ;;
  esac
fi
if [ $tool = maven ]; then $mvn -B -DskipTests package; else $gradle --no-daemon -x test build; fi
if [ $preset = quarkus ]; then
  jar=target/quarkus-app/quarkus-run.jar; [ -f "$jar" ] || jar=build/quarkus-app/quarkus-run.jar
elif [ -z "$jar" ]; then
  jar=$(ls -t target/*.jar build/libs/*.jar 2>/dev/null | grep -Ev -e '-(plain|sources|javadoc|tests)\.jar$' | head -n 1)
fi
if [ -z "$jar" ] || [ ! -f "$jar" ]; then echo "envoryx: the build left no jar to run (${jar:-none found in target/ or build/libs/})" >&2; exit 1; fi
if [ -n "$jdwp" ]; then exec java "$jdwp" -jar "$jar"; fi
exec java -jar "$jar"`

// Command returns the argv of the container's main process. Every value comes from
// Normalize and is passed as an argument, never pasted into the script.
func (c JavaConfig) Command() []string {
	port := "0"
	if c.Debug {
		port = strconv.Itoa(c.DebugPort)
	}
	mode := c.Mode
	if c.Preset == "jar" {
		mode = JavaModeProduction // there is no framework dev mode to run
	}
	return []string{"sh", "-c", javaServeScript, "envoryx-java", c.Preset, mode, port, c.Jar}
}

// entryGuard blocks until the project has a build file: a blank project would send the
// build into a crash-loop.
func (c JavaConfig) entryGuard() string {
	return `until [ -e pom.xml ] || [ -e build.gradle ] || [ -e build.gradle.kts ]; do echo 'envoryx: waiting for pom.xml or build.gradle in /var/www/html - scaffold with a Java template, clone a repository or use the Java terminal'; sleep 5; done`
}

// WrappedCommand returns Command() behind the build-file guard, for containers whose
// server is the project's application. Further guards (the database wait the planner
// builds) run after it.
func (c JavaConfig) WrappedCommand(guards ...string) []string {
	return Guarded(c.Command(), "envoryx-serve", append([]string{c.entryGuard()}, guards...)...)
}

// Env returns the variables that tell the application where to listen, under the names
// Spring Boot (SERVER_*), Quarkus (QUARKUS_HTTP_*) and Micronaut read, plus HOST and PORT
// for everything else. Quarkus's Dev Services are switched off: they start databases
// through Docker, which the container has no access to, and the project's own services
// are injected anyway.
func (c JavaConfig) Env() []string {
	port := strconv.Itoa(c.Port)
	return []string{
		"HOST=0.0.0.0", "PORT=" + port,
		"SERVER_ADDRESS=0.0.0.0", "SERVER_PORT=" + port,
		"QUARKUS_HTTP_HOST=0.0.0.0", "QUARKUS_HTTP_PORT=" + port, "QUARKUS_DEVSERVICES_ENABLED=false",
		"MICRONAUT_SERVER_HOST=0.0.0.0", "MICRONAUT_SERVER_PORT=" + port,
	}
}
