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
	tool, _ := e.engine.Container(DBToolContainer)
	if tool.State != "running" || tool.Spec.Labels[docker.LabelSystem] != "dbtool" || tool.Spec.Network != DBToolNetwork {
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
	if !contains(selfNets, DBToolNetwork) {
		t.Fatalf("Envoryx must join the tool network to reach it: %v", selfNets)
	}
	if dial, _ := e.m.DBToolDial(ctx); dial != DBToolContainer+":8080" {
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
	if again, _ := e.engine.Container(DBToolContainer); again.ID != tool.ID {
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
	if _, ok := e.engine.Container(DBToolContainer); ok {
		t.Fatal("container must be removed when disabled")
	}
	networks, _ := e.engine.ListNetworks(ctx, true)
	for _, n := range networks {
		if n.Name == DBToolNetwork {
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
	tool, _ := e.engine.Container(DBToolContainer)
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
