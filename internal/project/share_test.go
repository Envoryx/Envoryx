package project

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func tunnelLogs(e *env, name string) {
	e.engine.Logs = map[string][]docker.LogLine{name: {
		{Text: "INF Requesting new quick Tunnel on trycloudflare.com..."},
		{Text: "INF |  https://brave-little-tunnel.trycloudflare.com                    |"},
	}}
}

func TestShareProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Demo", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	tunnelLogs(e, "envoryx-demo-share")
	for _, d := range []time.Duration{time.Minute, 25 * time.Hour} {
		if _, err := e.m.StartShare(ctx, id, d); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%s: %v", d, err)
		}
	}
	s, err := e.m.StartShare(ctx, id, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "online" || s.URL != "https://brave-little-tunnel.trycloudflare.com" || time.Until(s.ExpiresAt) < 29*time.Minute {
		t.Fatalf("share: %+v", s)
	}
	c, ok := e.engine.Container("envoryx-demo-share")
	if !ok || c.Spec.Image != shareImage || c.Spec.Network != "envoryx-demo" || c.Spec.RestartPolicy != "no" || c.Spec.Labels[labelShareExpires] == "" {
		t.Fatalf("tunnel container: %+v", c.Spec)
	}
	if got := strings.Join(c.Spec.Cmd, " "); got != "tunnel --no-autoupdate --url http://envoryx-demo-web:80" {
		t.Fatalf("cmd: %s", got)
	}
	// The tunnel is no service of the project.
	v, _ := e.m.Get(ctx, id)
	if len(v.Status.Warnings) != 0 {
		t.Fatalf("warnings: %v", v.Status.Warnings)
	}
	// Recreating the application for new variables keeps the address.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Env: &[]EnvVarRequest{{Key: "A", Value: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-demo-share"); !ok {
		t.Fatal("an update ended the share")
	}
	// Stopping the project ends it.
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-demo-share"); ok {
		t.Fatal("the share outlived the project's stop")
	}
	if s, _ := e.m.ShareStatus(ctx, id); s.Active {
		t.Fatalf("status after stop: %+v", s)
	}
	if _, err := e.m.StartShare(ctx, id, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("sharing a stopped project: %v", err)
	}
}

func TestShareOnBareMetalUsesTheHostNetwork(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Demo", true))
	if err != nil {
		t.Fatal(err)
	}
	tunnelLogs(e, "envoryx-demo-share")
	if _, err := e.m.StartShare(ctx, view.Project.ID, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-demo-share")
	if c.Spec.Network != "host" || !slices.Contains(c.Spec.Cmd, "http://127.0.0.1:20000") {
		t.Fatalf("bare metal tunnel: %+v", c.Spec)
	}
}

func TestExpiredSharesEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Demo", true))
	if err != nil {
		t.Fatal(err)
	}
	tunnelLogs(e, "envoryx-demo-share")
	s, err := e.m.StartShare(ctx, view.Project.ID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.m.expireShares(ctx, s.ExpiresAt.Add(-time.Minute), log)
	if _, ok := e.engine.Container("envoryx-demo-share"); !ok {
		t.Fatal("a running share ended early")
	}
	e.m.expireShares(ctx, s.ExpiresAt.Add(time.Second), log)
	if _, ok := e.engine.Container("envoryx-demo-share"); ok {
		t.Fatal("an expired share must end")
	}
}

// A running share is public; an allowlist added while it runs would not restrict it.
func TestAllowlistIsRefusedWhileShared(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Open", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	tunnelLogs(e, "envoryx-open-share")
	if _, err := e.m.StartShare(ctx, id, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{AllowIPs: []string{"127.0.0.1"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("allowlist while shared: %v", err)
	}
	// Other rules stay possible, and after the share the allowlist is accepted.
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{Headers: []store.HeaderRule{{Name: "X-Robots-Tag", Value: "noindex"}}}); err != nil {
		t.Fatalf("headers while shared: %v", err)
	}
	if err := e.m.StopShare(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{AllowIPs: []string{"127.0.0.1"}}); err != nil {
		t.Fatalf("allowlist after the share: %v", err)
	}
}
