package project

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// javaRequest is a project without PHP whose Spring Boot server is the application, on
// PostgreSQL.
func javaRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Java:          &JavaRequest{Version: "25", Config: runtime.JavaConfig{Server: true, Preset: "spring-boot"}},
		Database:      &DatabaseRequest{Type: "postgresql"},
		CreateStarter: true,
		Start:         start,
	}
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func TestJavaServerServesProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, javaRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-java")
	if !ok {
		t.Fatal("java container missing")
	}
	// Behind the build-file wait and the database wait, spring-boot:run in dev mode.
	if c.Spec.Cmd[0] != "sh" || !strings.Contains(c.Spec.Cmd[2], "[ -e pom.xml ]") || !slices.Contains(c.Spec.Cmd, "envoryx-java") || !slices.Contains(c.Spec.Cmd, "spring-boot") {
		t.Fatalf("java command: %q", c.Spec.Cmd)
	}
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-java:25" || c.Spec.User != "1000:1000" || c.Spec.WorkingDir != "/var/www/html" {
		t.Fatalf("java container: %+v", c.Spec)
	}
	env := c.Spec.Env
	jdbc := envValue(env, "SPRING_DATASOURCE_URL")
	if !strings.HasPrefix(jdbc, "jdbc:postgresql://database:5432/") || envValue(env, "QUARKUS_DATASOURCE_JDBC_URL") != jdbc || envValue(env, "JDBC_URL") != jdbc {
		t.Fatalf("jdbc env: %v", env)
	}
	if envValue(env, "SPRING_DATASOURCE_USERNAME") != envValue(env, "DB_USERNAME") || envValue(env, "SPRING_DATASOURCE_PASSWORD") == "" || envValue(env, "QUARKUS_DATASOURCE_DB_KIND") != "postgresql" {
		t.Fatalf("datasource credentials: %v", env)
	}
	for _, want := range []string{"SERVER_PORT=8080", "QUARKUS_DEVSERVICES_ENABLED=false", "MAVEN_OPTS=-Dmaven.repo.local=/var/cache/envoryx/maven", "GRADLE_USER_HOME=/var/cache/envoryx/gradle", "HOME=/home/envoryx"} {
		if !slices.Contains(env, want) {
			t.Fatalf("java env lacks %s: %v", want, env)
		}
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 8080 {
		t.Fatalf("server port: %+v", c.Spec.Ports)
	}
	web, _ := e.engine.Container("envoryx-shop-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must stay unpublished: %+v", web.Spec.Ports)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "index.html")); err == nil {
		t.Fatal("no starter page while the Java server serves the project")
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "java" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	if kind, _ := AppKind(proj); kind != store.ServiceJava {
		t.Fatalf("AppKind = %s", kind)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range table.Routes {
		if r.ProjectID == proj.ID && r.Dial == "envoryx-shop-java:8080" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no route to the Java server: %+v", table.Routes)
	}
	target, err := e.m.ResolveSSHUser(ctx, "shop.java")
	if err != nil || target.Kind != store.ServiceJava {
		t.Fatalf("ssh user: %+v %v", target, err)
	}
	if target, _ := e.m.ResolveSSHUser(ctx, "shop"); target.Kind != store.ServiceJava {
		t.Fatalf("the bare user lands in the Java container: %+v", target)
	}
}

// An additional database gets a <NAME>_JDBC_URL; MongoDB gets the Spring and Quarkus
// connection-string names; MariaDB speaks jdbc:mariadb.
func TestJavaEnvForOtherDatabases(t *testing.T) {
	proj := store.Project{Services: []store.ProjectService{
		{Kind: store.ServiceDatabase, Variant: "mongodb", Enabled: true},
		{Kind: store.DatabaseKind("reports"), Variant: "mariadb", Enabled: true},
	}}
	env := javaEnv(proj, []string{
		"MONGODB_URI=mongodb://u:p@database:27017/shop?authSource=admin", "MONGODB_DATABASE=shop",
		"REPORTS_DB_HOST=reports", "REPORTS_DB_PORT=3306", "REPORTS_DB_DATABASE=reports",
		"REDIS_URL=redis://redis:6379", "SMTP_HOST=mailpit", "SMTP_PORT=1025",
	})
	for key, want := range map[string]string{
		"SPRING_DATA_MONGODB_URI":           "mongodb://u:p@database:27017/shop?authSource=admin",
		"QUARKUS_MONGODB_CONNECTION_STRING": "mongodb://u:p@database:27017/shop?authSource=admin",
		"REPORTS_JDBC_URL":                  "jdbc:mariadb://reports:3306/reports",
		"SPRING_DATA_REDIS_URL":             "redis://redis:6379",
		"QUARKUS_MAILER_PORT":               "1025",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if envValue(env, "SPRING_DATASOURCE_URL") != "" {
		t.Fatal("an additional database doesn't become Spring's data source")
	}
}

// JDWP publishes a port of its own once debugging is on; switching it recreates the
// container, and the server port survives the edit.
func TestJavaDebugAndUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, javaRequest("Dbg", true))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.engine.Container("envoryx-dbg-java")
	hostPort := before.Spec.Ports[0].HostPort
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Java: &JavaUpdate{Enabled: true, Version: "25", Config: runtime.JavaConfig{Server: true, Preset: "quarkus", Debug: true}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-dbg-java")
	if !slices.Contains(c.Spec.Cmd, "quarkus") || !slices.Contains(c.Spec.Cmd, "5005") || len(c.Spec.Ports) != 2 || c.Spec.Ports[0].HostPort != hostPort || c.Spec.Ports[1].ContainerPort != runtime.DefaultJDWPPort {
		t.Fatalf("debug: cmd %q ports %+v", c.Spec.Cmd, c.Spec.Ports)
	}
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Java: &JavaUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-dbg-java"); ok {
		t.Fatal("java container must be gone")
	}
}

func TestJavaTemplateWorkersTestsAndManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := javaRequest("My Shop", false)
	req.Java.Config = runtime.JavaConfig{}
	req.Template = "spring-boot"
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var steps [][]string
	for _, s := range e.engine.OneShots {
		if s.Image == "ghcr.io/envoryx/envoryx-java:25" {
			steps = append(steps, s.Cmd)
		}
	}
	if len(steps) != 3 || steps[0][2] != javaScaffoldScript || !strings.Contains(steps[1][2], "ddl-auto=update") || !strings.Contains(steps[2][2], "mvnw") {
		t.Fatalf("template steps: %q", steps)
	}
	u, err := url.Parse(steps[0][4])
	if err != nil || u.Host != "start.spring.io" {
		t.Fatalf("scaffold url: %q", steps[0][4])
	}
	q := u.Query()
	if q.Get("javaVersion") != "25" || q.Get("artifactId") != "my-shop" || q.Get("packageName") != "com.example.my_shop" || q.Get("dependencies") != "web,actuator,devtools,data-jpa,postgresql" {
		t.Fatalf("scaffold query: %v", q)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	cfg, _ := javaConfig(proj)
	if !cfg.Server || cfg.Preset != "spring-boot" || cfg.Port != 8080 {
		t.Fatalf("template merged into the config: %+v", cfg)
	}

	// A jar worker and a build tool goal from the Java image, with the injected datasource.
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "jobs", Preset: "java:jar", Arg: "./target/worker.jar", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, ok := e.engine.Container("envoryx-my-shop-worker-jobs")
	if !ok || w.Spec.Image != "ghcr.io/envoryx/envoryx-java:25" || !slices.Equal(w.Spec.Cmd, []string{"java", "-jar", "target/worker.jar"}) || envValue(w.Spec.Env, "SPRING_DATASOURCE_URL") == "" {
		t.Fatalf("java worker: %+v", w.Spec)
	}
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "main", Preset: "java:task", Arg: "exec:java -Dexec.mainClass=com.example.Worker", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, _ = e.engine.Container("envoryx-my-shop-worker-main")
	if w.Spec.Cmd[2] != javaTaskScript || !slices.Equal(w.Spec.Cmd[4:], []string{"exec:java", "-Dexec.mainClass=com.example.Worker"}) {
		t.Fatalf("java task worker: %q", w.Spec.Cmd)
	}
	for _, bad := range []WorkerRequest{
		{Name: "bad1", Preset: "java:task", Arg: "run; rm -rf /", Enabled: true},
		{Name: "bad2", Preset: "java:task", Arg: "--init-script /tmp/x.gradle", Enabled: true},
		{Name: "bad3", Preset: "java:jar", Arg: "../elsewhere.jar", Enabled: true},
	} {
		if _, err := e.m.AddWorker(ctx, v.Project.ID, bad); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}

	// mvn test once there's a pom.xml.
	dir := filepath.Join(e.projDir, "my-shop")
	_ = os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>\n"), 0o644)
	suites, err := e.m.TestSuites(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var maven TestSuite
	for _, s := range suites {
		if s.ID == "maven" {
			maven = s
		}
	}
	if maven.Service != store.ServiceJava || !maven.Report {
		t.Fatalf("java suites: %+v", suites)
	}
	argv, _ := maven.build("OrderTest#pays", "/tmp/report.xml")
	if argv[2] != javaTestScript || !slices.Equal(argv[4:], []string{"maven", "/tmp/report.xml", "OrderTest#pays"}) {
		t.Fatalf("maven argv %q", argv)
	}

	mf, err := e.m.ExportManifest(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Java == nil || !mf.Java.Server || mf.Java.Version != "25" || mf.Java.Preset != "spring-boot" {
		t.Fatalf("manifest java: %+v", mf.Java)
	}
	req2 := manifestRequest(manifest.Manifest{Version: 1, Java: &manifest.Java{Version: "21", Server: true, Preset: "jar", Jar: "build/libs/app.jar", Port: 9000}}, "From Manifest")
	if req2.Java == nil || req2.Java.Config.Preset != "jar" || req2.Java.Config.Jar != "build/libs/app.jar" || req2.Java.Config.Port != 9000 {
		t.Fatalf("manifest import: %+v", req2.Java)
	}
}

// TestJavaTestScript runs the test script against a stand-in mvn: every database URL
// gains _test before the run (before the parameters of one that has them), and the
// per-class reports end up in one file.
func TestJavaTestScript(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	mvn := `#!/bin/sh
mkdir -p target/surefire-reports
printf '<?xml version="1.0"?>\n<testsuite name="A"><testcase name="a"/></testsuite>\n' > target/surefire-reports/TEST-A.xml
printf '<?xml version="1.0"?>\n<testsuite name="B"><testcase name="b"/></testsuite>\n' > target/surefire-reports/TEST-B.xml
printf '%s|%s|%s|%s\n' "$*" "$SPRING_DATASOURCE_URL" "$JDBC_URL" "$DATABASE_URL"
exit 3
`
	if err := os.WriteFile(filepath.Join(bin, "mvn"), []byte(mvn), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale report from an earlier run must not survive.
	_ = os.MkdirAll(filepath.Join(dir, "target", "surefire-reports"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "target", "surefire-reports", "TEST-Old.xml"), []byte("<testsuite name=\"Old\"/>"), 0o644)
	report := filepath.Join(dir, "report.xml")
	cmd := exec.Command("sh", "-c", javaTestScript, "envoryx-maven-test", "maven", report, "OrderTest")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "SPRING_DATASOURCE_URL=jdbc:postgresql://database:5432/shop", "JDBC_URL=jdbc:mysql://database:3306/shop?useSSL=false&allowPublicKeyRetrieval=true", "DATABASE_URL=mongodb://u:p@database/shop?authSource=admin"}
	out, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("the test run's exit code must come through: %v", err)
	}
	if want := "-B test -Dtest=OrderTest -Dsurefire.failIfNoSpecifiedTests=false|jdbc:postgresql://database:5432/shop_test|jdbc:mysql://database:3306/shop_test?useSSL=false&allowPublicKeyRetrieval=true|mongodb://u:p@database/shop?authSource=admin\n"; string(out) != want {
		t.Fatalf("got %q", out)
	}
	merged, _ := os.ReadFile(report)
	res, err := parseJUnit(strings.NewReader(string(merged)), dir)
	if err != nil || strings.Contains(string(merged), "Old") || strings.Contains(string(merged), "<?xml") || res.Tests != 2 {
		t.Fatalf("merged report %q: %+v %v", merged, res, err)
	}
}

func TestJavaPackageNameAndActions(t *testing.T) {
	for slug, want := range map[string]string{"shop-api": "shop_api", "new": "app_new", "42shop": "app_42shop"} {
		if got := javaPackageName(slug); got != want {
			t.Errorf("%s: %s, want %s", slug, got, want)
		}
	}
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, javaRequest("Tools", true))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "tools", "build.gradle.kts"), []byte(""), 0o644)
	actions, err := e.m.ListActions(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	avail := map[string]string{}
	for _, a := range actions {
		avail[a.ID] = a.Reason
	}
	if r, ok := avail["gradle:build"]; !ok || r != "" {
		t.Fatalf("gradle:build takes build.gradle.kts: %q", r)
	}
	if r := avail["maven:package"]; r != "pom.xml not found in project" {
		t.Fatalf("maven:package without pom.xml: %q", r)
	}
}
