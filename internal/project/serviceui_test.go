package project

import (
	"context"
	"errors"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestServiceWebUIsThroughTheProxy(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Mail", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.Update(ctx, id, UpdateRequest{Mailpit: &ExtraUpdate{Enabled: new(true)}, Meilisearch: &ExtraUpdate{Enabled: new(true)}}); err != nil {
		t.Fatal(err)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inbox := table.Routes[ServiceUIHostname("mail", "mailpit", DefaultBaseDomain)]
	if inbox.Login != LoginAddon || inbox.Dial != "envoryx-mail-mailpit:8025" || inbox.ProjectID != id {
		t.Fatalf("mailpit route: %+v", inbox)
	}
	// Meilisearch checks its key itself, and the app's pages may search with one.
	if search := table.Routes[ServiceUIHostname("mail", "meilisearch", DefaultBaseDomain)]; search.Login != "" || search.Dial != "envoryx-mail-meilisearch:7700" {
		t.Fatalf("meilisearch route: %+v", search)
	}
	if _, ok := table.Routes[ServiceUIHostname("mail", "rabbitmq", DefaultBaseDomain)]; ok {
		t.Fatal("no route for a service the project doesn't have")
	}

	// The UI gets the address.
	infos, err := e.m.ExtraServices(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Kind == store.ServiceMailpit && in.WebUIURL != "" {
			t.Fatalf("without proxy ports there is no address: %q", in.WebUIURL)
		}
	}
	e.m.SetLinks(func(context.Context) (string, int, int) { return "", 80, 443 })
	infos, _ = e.m.ExtraServices(ctx, id)
	found := false
	for _, in := range infos {
		if in.Kind == store.ServiceMailpit {
			found = in.WebUIURL == "https://mail-mailpit."+DefaultBaseDomain
		}
	}
	if !found {
		t.Fatalf("mailpit address missing: %+v", infos)
	}

	// The names are taken for every project, also for a service it doesn't run yet.
	other, err := e.m.Create(ctx, phpRequest("Other", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddDomain(ctx, other.Project.ID, ServiceUIHostname("mail", "rabbitmq", DefaultBaseDomain)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("taken name: %v", err)
	}
}

func TestPortsPublishedOnTheLocalInterface(t *testing.T) {
	e := newEnv(t)
	e.publish = "127.0.0.1"
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Local", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Mailpit: &ExtraUpdate{Enabled: new(true)}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"envoryx-local-web", "envoryx-local-mailpit"} {
		c, ok := e.engine.Container(name)
		if !ok || len(c.Spec.Ports) == 0 {
			t.Fatalf("%s: %+v", name, c.Spec.Ports)
		}
		for _, p := range c.Spec.Ports {
			if p.HostIP != "127.0.0.1" {
				t.Fatalf("%s publishes on %q", name, p.HostIP)
			}
		}
	}
}

func TestProjectBehindTheSignIn(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()
	v, err := e.m.Create(ctx, nodeRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.AddDomain(ctx, id, "app.example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{Login: true, BasicAuth: &BasicAuthRequest{User: "client", Password: "s3cret"}, Headers: []store.HeaderRule{{Name: "X-Test", Value: "1"}}}); err != nil {
		t.Fatal(err)
	}
	e.m.shareHosts.Store(id, "random-words.trycloudflare.com")
	defer e.m.shareHosts.Delete(id)
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"shop.test", "app.example.test", "shop-dev.test"} {
		r := table.Routes[host]
		if r.Login != LoginApp || r.Rules == nil || r.Rules.BasicAuth != nil || len(r.Rules.Headers) != 1 {
			t.Fatalf("%s: %+v rules %+v", host, r, r.Rules)
		}
	}
	// The pages load their scripts from the dev server, which gets a pass on the way.
	if also := table.Routes["app.example.test"].LoginAlso; len(also) != 1 || also[0] != "shop-dev.test" {
		t.Fatalf("login also: %v", also)
	}
	// The share stays open, with the basic authentication.
	if r := table.Routes["random-words.trycloudflare.com"]; r.Login != "" || r.Rules == nil || r.Rules.BasicAuth == nil {
		t.Fatalf("share: %+v", r)
	}
	if v, _ := e.m.Get(ctx, id); !v.Project.ProxyRules.Login {
		t.Fatal("login not stored")
	}

	// Webhooks skip the sign-in.
	for _, bad := range []string{"webhooks", "/a/../b", "/a b", "/x*y", "/a%2f"} {
		if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{Login: true, OpenPaths: []string{bad}}); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{Login: true, OpenPaths: []string{" /webhooks/* ", "/up", "/up"}}); err != nil {
		t.Fatal(err)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if open := table.Routes["shop.test"].LoginExcept; len(open) != 2 || open[0] != "/webhooks/*" {
		t.Fatalf("open paths: %v", open)
	}
}
