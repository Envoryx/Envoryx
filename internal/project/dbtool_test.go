package project

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestDBToolLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	selfID := e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx", Labels: map[string]string{"x": "y"}}, "running")
	e.selfID = selfID

	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// Off by default.
	if _, err := e.m.OpenDBTool(ctx, id, ""); !errors.Is(err, ErrDBToolDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if st, _ := e.m.DBToolStatus(ctx); st.Enabled || st.Running {
		t.Fatalf("status: %+v", st)
	}
	if err := e.m.SetDBToolEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	// First use starts the container, writes the credentials and joins the project network.
	link, err := e.m.OpenDBTool(ctx, id, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.HasPrefix(link.URL, "/dbtool/?") || !strings.Contains(link.URL, "server=envoryx-shop-database") || !strings.Contains(link.URL, "username=") || link.Server != "envoryx-shop-database" {
		t.Fatalf("link: %+v", link)
	}
	tool, _ := e.engine.Container(DBToolName(testInstance))
	if tool.State != "running" || tool.Spec.Labels[docker.LabelSystem] != "dbtool" || tool.Spec.Network != DBToolName(testInstance) {
		t.Fatalf("tool container: %+v", tool)
	}
	if len(tool.Spec.Ports) != 0 {
		t.Fatalf("inside Docker no host port is published: %+v", tool.Spec.Ports)
	}
	nets, _ := e.engine.ContainerNetworks(ctx, tool.ID)
	if !contains(nets, NetworkName("shop")) {
		t.Fatalf("tool must join the project network: %v", nets)
	}
	selfNets, _ := e.engine.ContainerNetworks(ctx, selfID)
	if !contains(selfNets, DBToolName(testInstance)) {
		t.Fatalf("Envoryx must join the tool network to reach it: %v", selfNets)
	}
	if dial, _ := e.m.DBToolDial(ctx); dial != DBToolName(testInstance)+":8080" {
		t.Fatalf("dial = %q", dial)
	}
	raw, err := os.ReadFile(filepath.Join(e.cfgDir, "dbtool", "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	var conns map[string]dbToolConnection
	if err := json.Unmarshal(raw, &conns); err != nil {
		t.Fatal(err)
	}
	creds, _ := e.m.DatabaseCredentials(ctx, id, "")
	c, ok := conns["server|envoryx-shop-database|"+creds.Username]
	if !ok || c.Password != creds.Password || c.Database != creds.Database {
		t.Fatalf("connections file: %+v", conns)
	}
	if _, ok := conns["server|envoryx-shop-database|root"]; !ok {
		t.Fatalf("root login must be offered too: %+v", conns)
	}
	if plugin, err := os.ReadFile(filepath.Join(e.cfgDir, "dbtool", "plugins", "envoryx.php")); err != nil || !strings.Contains(string(plugin), "class AdminerEnvoryx") {
		t.Fatalf("plugin file: %v", err)
	}

	// The tool is managed but belongs to no project: never an orphan.
	report := e.m.Reconcile(ctx)
	if len(report.Orphans) != 0 {
		t.Fatalf("orphans: %+v", report.Orphans)
	}
	// Second open is idempotent.
	if _, err := e.m.OpenDBTool(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.engine.Container(DBToolName(testInstance)); again.ID != tool.ID {
		t.Fatal("open must reuse the running container")
	}

	// Deleting the project detaches the tool so the network can go.
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "shop"}); err != nil {
		t.Fatalf("delete with tool attached: %v", err)
	}
	nets, _ = e.engine.ContainerNetworks(ctx, tool.ID)
	if contains(nets, NetworkName("shop")) {
		t.Fatalf("tool still attached: %v", nets)
	}

	// Switching off removes everything.
	if err := e.m.SetDBToolEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container(DBToolName(testInstance)); ok {
		t.Fatal("container must be removed when disabled")
	}
	networks, _ := e.engine.ListNetworks(ctx, true)
	for _, n := range networks {
		if n.Name == DBToolName(testInstance) {
			t.Fatal("tool network must be removed when disabled")
		}
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "dbtool", "connections.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credentials file must be removed when disabled")
	}
}

func TestDBToolBareMetalAndUnsupported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.m.SetDBToolEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	view, err := e.m.Create(ctx, dbRequest("Local", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.OpenDBTool(ctx, view.Project.ID, ""); err != nil {
		t.Fatal(err)
	}
	tool, _ := e.engine.Container(DBToolName(testInstance))
	if len(tool.Spec.Ports) != 1 || tool.Spec.Ports[0].HostIP != "127.0.0.1" || tool.Spec.Ports[0].ContainerPort != 8080 {
		t.Fatalf("bare metal must publish on loopback: %+v", tool.Spec.Ports)
	}
	if dial, _ := e.m.DBToolDial(ctx); dial != "127.0.0.1:"+strconv.Itoa(tool.Spec.Ports[0].HostPort) {
		t.Fatalf("dial = %q", dial)
	}

	req := phpRequest("Docs", true)
	req.Database = &DatabaseRequest{Type: "mongodb", Version: "8"}
	mongo, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.OpenDBTool(ctx, mongo.Project.ID, ""); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("mongodb must be refused with a hint: %v", err)
	}
	plain, _ := e.m.Create(ctx, phpRequest("NoDB", false))
	if _, err := e.m.OpenDBTool(ctx, plain.Project.ID, ""); err == nil {
		t.Fatal("a project without database has nothing to open")
	}
}

func TestDBToolProjectsMapsTargetsToTheirProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	shop, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	req := phpRequest("Blog", false)
	req.Database = &DatabaseRequest{Type: "postgresql"}
	blog, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	shopCreds, _ := e.m.DatabaseCredentials(ctx, shop.Project.ID, "")
	blogCreds, _ := e.m.DatabaseCredentials(ctx, blog.Project.ID, "")

	for _, c := range []struct {
		name   string
		target DBToolTarget
		want   string
	}{
		{"app user", DBToolTarget{"server", "envoryx-shop-database", shopCreds.Username}, shop.Project.ID},
		{"root", DBToolTarget{"server", "envoryx-shop-database", "root"}, shop.Project.ID},
		{"login page of a server", DBToolTarget{"server", "envoryx-shop-database", ""}, shop.Project.ID},
		{"postgres app user", DBToolTarget{"pgsql", "envoryx-blog-database", blogCreds.Username}, blog.Project.ID},
		{"postgres administrator", DBToolTarget{"pgsql", "envoryx-blog-database", "postgres"}, blog.Project.ID},
		{"another project's user on the server", DBToolTarget{"server", "envoryx-shop-database", blogCreds.Username}, ""},
		{"wrong driver", DBToolTarget{"pgsql", "envoryx-shop-database", "root"}, ""},
		{"unknown server", DBToolTarget{"server", "db.example.com", "root"}, ""},
		{"no server", DBToolTarget{"server", "", "root"}, ""},
	} {
		ids, err := e.m.DBToolProjects(ctx, c.target)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" && len(ids) != 0 || c.want != "" && (len(ids) != 1 || ids[0] != c.want) {
			t.Errorf("%s: %v, want %q", c.name, ids, c.want)
		}
	}
}

func TestDBToolContainerAnswersOnlyTheProxy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.selfID = e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx", Labels: map[string]string{"x": "y"}}, "running")
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.SetDBToolEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	// A container from before the router is not used and the reconcile removes it.
	e.engine.AddManagedContainer(docker.ContainerSpec{Name: DBToolName(testInstance), Image: DBToolImage,
		Labels: map[string]string{docker.LabelManaged: "true", docker.LabelSystem: "dbtool", dbToolHostsLabel: "1"}}, "running")
	if dial, _ := e.m.DBToolDial(ctx); dial != "" {
		t.Fatalf("an unguarded container must not be proxied to: %q", dial)
	}
	e.m.Reconcile(ctx)
	if _, ok := e.engine.Container(DBToolName(testInstance)); ok {
		t.Fatal("the unguarded container must be removed")
	}

	if _, err := e.m.OpenDBTool(ctx, view.Project.ID, ""); err != nil {
		t.Fatal(err)
	}
	tool, _ := e.engine.Container(DBToolName(testInstance))
	if tool.Spec.Labels[dbToolGuardLabel] != "1" || len(tool.Spec.Cmd) == 0 || tool.Spec.Cmd[len(tool.Spec.Cmd)-1] != "/envoryx/router.php" {
		t.Fatalf("the container must run behind the router: %+v %v", tool.Spec.Labels, tool.Spec.Cmd)
	}
	router, err := os.ReadFile(filepath.Join(e.cfgDir, "dbtool", "router.php"))
	if err != nil || !strings.Contains(string(router), "HTTP_X_ENVORYX_DBTOOL_TOKEN") {
		t.Fatalf("router script: %v", err)
	}
	token, err := e.m.DBToolProxyToken()
	if err != nil || len(token) != 64 {
		t.Fatalf("token: %q %v", token, err)
	}
	if _, err := e.m.OpenDBTool(ctx, view.Project.ID, ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.m.DBToolProxyToken(); again != token {
		t.Fatal("the token must stay while the container lives")
	}
	if err := e.m.SetDBToolEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.DBToolProxyToken(); err == nil {
		t.Fatal("switching off must remove the token")
	}
}
