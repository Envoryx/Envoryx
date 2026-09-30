package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/project"
)

func targetKey(t *project.DBToolTarget) string {
	if t == nil {
		return ""
	}
	return t.Key()
}

// The proxy must read an Adminer request the way PHP and Adminer do, or refuse it.
func TestAdminerTargets(t *testing.T) {
	for _, c := range []struct {
		name, method, query, form string
		target, login             string // Key(), "" for none
		refused                   bool
	}{
		{name: "mysql", query: "server=envoryx-shop-database&username=shop&db=shop", target: "server|envoryx-shop-database|shop"},
		{name: "postgres with schema", query: "pgsql=envoryx-blog-database&username=blog&db=blog&ns=public", target: "pgsql|envoryx-blog-database|blog"},
		{name: "login page of a server (after logout)", query: "pgsql=envoryx-blog-database", target: "pgsql|envoryx-blog-database|"},
		{name: "user only means Adminer's default driver", query: "username=root", target: "server||root"},
		{name: "sqlite is a driver too", query: "sqlite=/envoryx/connections.json&username=x", target: "sqlite|/envoryx/connections.json|x"},
		{name: "start page", query: ""},
		{name: "static file", query: "file=default.css&version=5.5.1"},
		{name: "repeated equal values", query: "server=a&server=a&username=u", target: "server|a|u"},
		{name: "repeated different values", query: "server=a&server=b&username=u", refused: true},
		{name: "two drivers", query: "server=a&pgsql=b&username=u", refused: true},
		{name: "array parameter", query: "server[]=a&username=u", refused: true},
		{name: "array username", query: "server=a&username[x]=u", refused: true},
		{name: "leading space PHP drops", query: "%20server=b&server=a&username=u", refused: true},
		{name: "NUL byte PHP cuts at", query: "server%00x=b&server=a&username=u", refused: true},
		{name: "bad escape", query: "server=a&username=%zz", refused: true},
		{name: "semicolon", query: "server=a;username=u", refused: true},
		{name: "login post", method: "POST", form: "auth[driver]=server&auth[server]=a&auth[username]=u&auth[password]=p&auth[db]=", login: "server|a|u"},
		{name: "login post on a page", method: "POST", query: "server=a&username=u", form: "auth[driver]=server&auth[server]=b&auth[username]=u&auth[password]=p", target: "server|a|u", login: "server|b|u"},
		{name: "login post with nested field", method: "POST", form: "auth[server][x]=b", refused: true},
		{name: "ordinary form post", method: "POST", query: "server=a&username=u&sql=", form: "query=SELECT+1&token=1", target: "server|a|u"},
	} {
		method := c.method
		if method == "" {
			method = "GET"
		}
		r := httptest.NewRequest(method, "/dbtool/?"+c.query, strings.NewReader(c.form))
		if c.form != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		}
		target, login, err := adminerTargets(r)
		if c.refused {
			if err == nil {
				t.Errorf("%s: not refused (%v, %v)", c.name, target, login)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := targetKey(target); got != c.target {
			t.Errorf("%s: target %q, want %q", c.name, got, c.target)
		}
		if got := targetKey(login); got != c.login {
			t.Errorf("%s: login %q, want %q", c.name, got, c.login)
		}
		if c.form != "" {
			// The body still reaches Adminer in full.
			if body, err := io.ReadAll(r.Body); err != nil || string(body) != c.form {
				t.Errorf("%s: body after the check: %q %v", c.name, body, err)
			}
		}
	}
}
