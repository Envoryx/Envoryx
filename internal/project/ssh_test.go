package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

// The bare SSH user reaches the project's application container - PHP when present, Node
// otherwise - so IDE configurations only need the slug; suffixes select explicitly.
func TestResolveSSHUserPicksTheApplicationContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	both := phpRequest("Shop", true)
	both.Node = &NodeRequest{Version: "24"}
	for _, req := range []CreateRequest{both, nodeRequest("Front", true), staticRequest("Site", true)} {
		if _, err := e.m.Create(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		user      string
		kind      store.ServiceKind
		container string
	}{
		{"shop", store.ServicePHP, "envoryx-shop-php"},
		{"shop.php", store.ServicePHP, "envoryx-shop-php"},
		{"shop.node", store.ServiceNode, "envoryx-shop-node"},
		{"front", store.ServiceNode, "envoryx-front-node"},
		{"front.node", store.ServiceNode, "envoryx-front-node"},
	}
	for _, tc := range cases {
		target, err := e.m.ResolveSSHUser(ctx, tc.user)
		if err != nil {
			t.Fatalf("%s: %v", tc.user, err)
		}
		if target.Kind != tc.kind || target.ContainerName != tc.container || !target.Running || target.User != "1000:1000" || target.WorkingDir != appMountTarget {
			t.Fatalf("%s: %+v", tc.user, target)
		}
	}
	if _, err := e.m.ResolveSSHUser(ctx, "front.php"); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "no php service") {
		t.Fatalf("front.php: %v", err)
	}
	// A static site gets a files-only target (SFTP), but no explicit runtime.
	if target, err := e.m.ResolveSSHUser(ctx, "site"); err != nil || !target.Static || target.Kind != "" || target.ContainerID != "" || target.Gateway || target.Mounts[appMountTarget] == "" {
		t.Fatalf("static project: %+v %v", target, err)
	}
	for _, user := range []string{"site.php", "site.node"} {
		if _, err := e.m.ResolveSSHUser(ctx, user); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: %v", user, err)
		}
	}
	for _, user := range []string{"nope", "Shop", "shop.web", "../x"} {
		if _, err := e.m.ResolveSSHUser(ctx, user); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%q must be unknown: %v", user, err)
		}
	}
}
