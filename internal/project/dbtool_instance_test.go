package project

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

// Docker names are host-wide: every instance on a host needs its own browser name.
func TestDBToolNameIsPerInstance(t *testing.T) {
	a, b := DBToolName("3f9a2c7e11d04b6a9e8f7c6d5b4a3f21"), DBToolName("0b1c2d3e4f5061728394a5b6c7d8e9f0")
	if a != "envoryx-dbtool-3f9a2c7e" || b != "envoryx-dbtool-0b1c2d3e" {
		t.Fatalf("generated IDs: %q %q", a, b)
	}
	if DBToolName("") != legacyDBToolName {
		t.Fatalf("no instance: %q", DBToolName(""))
	}
	// Hand-written IDs may share their first characters.
	if p, s := DBToolName("envoryx-prod"), DBToolName("envoryx-staging"); p == s || p != "envoryx-dbtool-envoryx-prod" {
		t.Fatalf("hand-written IDs: %q %q", p, s)
	}
	long := strings.Repeat("a", 60)
	l1, l2 := DBToolName(long+"-one"), DBToolName(long+"-two")
	if l1 == l2 || len(l1) > 63 || len(l2) > 63 {
		t.Fatalf("long IDs: %q %q", l1, l2)
	}
}

// legacyDBTool adds a browser container and network under the host-wide name of older
// versions, labelled for instance ("" for one from before instance labels).
func legacyDBTool(e *env, instance string) {
	labels := map[string]string{docker.LabelManaged: "true", docker.LabelSystem: "dbtool", docker.LabelService: "dbtool"}
	if instance != "" {
		labels[docker.LabelInstance] = instance
	}
	e.engine.AddNetwork(legacyDBToolName, maps.Clone(labels))
	labels[dbToolHostsLabel], labels[dbToolGuardLabel] = "1", "1"
	e.engine.AddManagedContainer(docker.ContainerSpec{Name: legacyDBToolName, Image: DBToolImage, Labels: labels, Network: legacyDBToolName}, "running")
}

func networkExists(t *testing.T, e *env, name string) bool {
	t.Helper()
	networks, err := e.engine.ListNetworks(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range networks {
		if n.Name == name {
			return true
		}
	}
	return false
}

// dbToolEnv is an Envoryx inside Docker with a MySQL project and the browser switched on.
func dbToolEnv(t *testing.T) (*env, string) {
	t.Helper()
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
	return e, view.Project.ID
}

// This instance's browser under the old host-wide name moves to its own name on the next
// open, without a manual step; the reconcile retires it before that.
func TestDBToolMovesOffTheHostWideName(t *testing.T) {
	for _, via := range []string{"open", "reconcile"} {
		for _, instance := range []string{"unlabelled", testInstance} {
			t.Run(via+"/"+instance, func(t *testing.T) {
				e, id := dbToolEnv(t)
				ctx := context.Background()
				legacyDBTool(e, strings.TrimPrefix(instance, "unlabelled"))
				if dial, _ := e.m.DBToolDial(ctx); dial != "" {
					t.Fatalf("the old container must not be proxied to: %q", dial)
				}
				if st, _ := e.m.DBToolStatus(ctx); st.Running {
					t.Fatalf("the old container does not count as running: %+v", st)
				}
				if via == "reconcile" {
					e.m.Reconcile(ctx)
					if _, ok := e.engine.Container(legacyDBToolName); ok || networkExists(t, e, legacyDBToolName) {
						t.Fatal("the reconcile must retire the old container and network")
					}
				}
				if _, err := e.m.OpenDBTool(ctx, id, ""); err != nil {
					t.Fatal(err)
				}
				if _, ok := e.engine.Container(legacyDBToolName); ok {
					t.Fatal("the old container must be gone")
				}
				if networkExists(t, e, legacyDBToolName) {
					t.Fatal("the old network must be gone")
				}
				name := DBToolName(testInstance)
				tool, ok := e.engine.Container(name)
				if !ok || tool.State != "running" || tool.Spec.Network != name {
					t.Fatalf("new container: %+v", tool)
				}
				nets, _ := e.engine.ContainerNetworks(ctx, tool.ID)
				if !contains(nets, NetworkName("shop")) {
					t.Fatalf("the new container must join the project network: %v", nets)
				}
				if selfNets, _ := e.engine.ContainerNetworks(ctx, e.selfID); !contains(selfNets, name) || contains(selfNets, legacyDBToolName) {
					t.Fatalf("Envoryx networks: %v", selfNets)
				}
				if dial, _ := e.m.DBToolDial(ctx); dial != name+":8080" {
					t.Fatalf("dial = %q", dial)
				}
				if st, _ := e.m.DBToolStatus(ctx); !st.Running {
					t.Fatalf("status: %+v", st)
				}
			})
		}
	}
}

// Another instance's browser keeps the old name; this instance gets its own next to it.
func TestDBToolLeavesAnotherInstancesBrowserAlone(t *testing.T) {
	e, id := dbToolEnv(t)
	ctx := context.Background()
	legacyDBTool(e, "other-instance")
	e.m.Reconcile(ctx)
	if _, err := e.m.OpenDBTool(ctx, id, ""); err != nil {
		t.Fatalf("open next to another instance's browser: %v", err)
	}
	if c, ok := e.engine.Container(legacyDBToolName); !ok || c.State != "running" {
		t.Fatalf("the other instance's container: %+v", c)
	}
	if !networkExists(t, e, legacyDBToolName) {
		t.Fatal("the other instance's network must stay")
	}
	if _, ok := e.engine.Container(DBToolName(testInstance)); !ok {
		t.Fatal("this instance's own browser is missing")
	}
	if err := e.m.SetDBToolEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container(legacyDBToolName); !ok || !networkExists(t, e, legacyDBToolName) {
		t.Fatal("switching off must not remove the other instance's browser")
	}
}
