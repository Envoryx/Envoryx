package project

import (
	"context"
	"errors"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
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
