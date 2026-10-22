package project

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestGuestLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.CreateGuestLink(ctx, id, "Client", 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("without the proxy there is no address: %v", err)
	}
	e.m.SetLinks(func(context.Context) (string, int, int) { return "", 80, 443 })
	for _, bad := range []struct {
		label string
		days  int
	}{{"", 7}, {"x", 91}, {"x", -1}, {strings.Repeat("x", 81), 1}} {
		if _, err := e.m.CreateGuestLink(ctx, id, bad.label, bad.days); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q %d: %v", bad.label, bad.days, err)
		}
	}
	l, err := e.m.CreateGuestLink(ctx, id, "Acme review", 0)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(l.URL)
	if err != nil || u.Scheme != "https" || u.Host != "shop."+DefaultBaseDomain || u.Path != proxy.GuestPath {
		t.Fatalf("url: %s", l.URL)
	}
	if days := l.ExpiresAt.Sub(l.CreatedAt).Hours() / 24; days != 7 {
		t.Fatalf("default lifetime: %v days", days)
	}
	projectID, ok := e.m.GuestPass(ctx, u.Query().Get("t"))
	if !ok || projectID != id {
		t.Fatalf("pass: %v %s", ok, projectID)
	}
	if _, ok := e.m.GuestPass(ctx, u.Query().Get("t")+"x"); ok {
		t.Fatal("a wrong token must not pass")
	}
	list, err := e.m.GuestLinks(ctx, id)
	if err != nil || len(list) != 1 || list[0].URL != "" || list[0].UsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := e.m.RevokeGuestLink(ctx, id, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.m.GuestPass(ctx, u.Query().Get("t")); ok {
		t.Fatal("a revoked link must not pass")
	}
}
