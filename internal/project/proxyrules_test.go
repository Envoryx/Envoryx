package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestProxyRulesAreCheckedAndStored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	for name, req := range map[string]ProxyRulesRequest{
		"address":        {AllowIPs: []string{"10.0.0.300"}},
		"no password":    {BasicAuth: &BasicAuthRequest{User: "client"}},
		"colon in user":  {BasicAuth: &BasicAuthRequest{User: "a:b", Password: "x"}},
		"relative from":  {Redirects: []store.RedirectRule{{From: "old", To: "/new"}}},
		"inner wildcard": {Redirects: []store.RedirectRule{{From: "/a/*/b", To: "/new"}}},
		"target scheme":  {Redirects: []store.RedirectRule{{From: "/a", To: "javascript:alert(1)"}}},
		"protocol rel":   {Redirects: []store.RedirectRule{{From: "/a", To: "//evil.test"}}},
		"loop":           {Redirects: []store.RedirectRule{{From: "/a", To: "/a"}}},
		"status":         {Redirects: []store.RedirectRule{{From: "/a", To: "/b", Status: 200}}},
		"header name":    {Headers: []store.HeaderRule{{Name: "Bad Header", Value: "x"}}},
		"reserved":       {Headers: []store.HeaderRule{{Name: "content-length", Value: "1"}}},
		"header twice":   {Headers: []store.HeaderRule{{Name: "X-A", Value: "1"}, {Name: "x-a", Value: "2"}}},
		"header newline": {Headers: []store.HeaderRule{{Name: "X-A", Value: "a\r\nSet-Cookie: x"}}},
		"origin":         {CORS: &store.CORSRule{Origins: []string{"https://app.test/path"}}},
		"star with cred": {CORS: &store.CORSRule{Origins: []string{"*"}, Credentials: true}},
	} {
		if _, err := e.m.SetProxyRules(ctx, id, req); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	req := ProxyRulesRequest{
		AllowIPs:  []string{" 192.168.1.7 ", "10.1.2.3/8", "::ffff:172.16.0.1"},
		BasicAuth: &BasicAuthRequest{User: "client", Password: "s3cret"},
		Redirects: []store.RedirectRule{{Host: "WWW.Shop.Test", From: "/*", To: "https://shop.test/*", Status: 301}, {From: "/old", To: "/new"}},
		Headers:   []store.HeaderRule{{Name: "x-frame-options", Value: "DENY"}, {Name: "X-Powered-By"}},
		CORS:      &store.CORSRule{Origins: []string{"https://App.test/", "https://*.shop.test"}, Methods: []string{"get", "post"}},
	}
	v, err := e.m.SetProxyRules(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}
	r := v.Project.ProxyRules
	if strings.Join(r.AllowIPs, " ") != "192.168.1.7/32 10.0.0.0/8 172.16.0.1/32" {
		t.Errorf("addresses: %v", r.AllowIPs)
	}
	if r.Redirects[0].Host != "www.shop.test" || r.Redirects[1].Status != 302 || r.Headers[0].Name != "X-Frame-Options" {
		t.Errorf("redirects and headers: %+v %+v", r.Redirects, r.Headers)
	}
	if strings.Join(r.CORS.Origins, " ") != "https://app.test https://*.shop.test" || strings.Join(r.CORS.Methods, " ") != "GET POST" {
		t.Errorf("cors: %+v", r.CORS)
	}
	hash := r.BasicAuth.PasswordHash
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")) != nil {
		t.Fatalf("password hash: %q", hash)
	}
	// Saving again without a password keeps it.
	req.BasicAuth.Password = ""
	v, err = e.m.SetProxyRules(ctx, id, req)
	if err != nil || v.Project.ProxyRules.BasicAuth.PasswordHash != hash {
		t.Fatalf("kept password: %v", err)
	}

	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rules := table.Routes["shop.test"].Rules
	if rules == nil || len(rules.Allow) != 3 || rules.BasicAuth == nil || rules.BasicAuth.Realm != "Shop" || len(rules.Redirects) != 2 || rules.CORS == nil {
		t.Fatalf("compiled rules: %+v", rules)
	}
	// An allowlist and a share exclude each other.
	tunnelLogs(e, "envoryx-shop-share")
	if _, err := e.m.StartShare(ctx, id, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("share behind an allowlist: %v", err)
	}
	// Empty rules switch everything off.
	v, err = e.m.SetProxyRules(ctx, id, ProxyRulesRequest{})
	if err != nil || !v.Project.ProxyRules.Empty() {
		t.Fatalf("cleared: %v %+v", err, v.Project.ProxyRules)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if table.Routes["shop.test"].Rules != nil {
		t.Fatal("rules after clearing")
	}
}

func TestShareGoesThroughTheProxy(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	e.m.SetShareProxy(":8080")
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Demo", true))
	if err != nil {
		t.Fatal(err)
	}
	tunnelLogs(e, "envoryx-demo-share")
	if _, err := e.m.SetProxyRules(ctx, view.Project.ID, ProxyRulesRequest{BasicAuth: &BasicAuthRequest{User: "client", Password: "pw"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.StartShare(ctx, view.Project.ID, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-demo-share")
	if got := strings.Join(c.Spec.Cmd, " "); got != "tunnel --no-autoupdate --url http://demo.test:8080" {
		t.Fatalf("cmd: %s", got)
	}
	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	share, ok := table.Routes["brave-little-tunnel.trycloudflare.com"]
	if !ok || !share.Share || share.Rules == nil || share.Rules.BasicAuth == nil {
		t.Fatalf("share route: %+v", share)
	}
	if aliases := e.m.proxyAliases(ctx); strings.Contains(strings.Join(aliases, " "), "trycloudflare") {
		t.Fatalf("the share address became a network alias: %v", aliases)
	}
	if err := e.m.StopShare(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if _, ok := table.Routes["brave-little-tunnel.trycloudflare.com"]; ok {
		t.Fatal("the route outlived the share")
	}
}
