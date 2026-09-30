package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

func TestRedactedConfigKeepsSecretsOut(t *testing.T) {
	const sealed = "TOP-SECRET-VALUE"
	svc := `{"hostPort":6379,"webUiPort":15672,"username":"envoryx","password":"` + sealed + `","apiKey":"` + sealed + `","host":"redis.example","port":6380}`
	cases := []struct {
		kind store.ServiceKind
		cfg  string
		want []string // fragments that must stay
	}{
		{store.ServiceRedis, svc, []string{`"host":"redis.example"`, `"port":6380`}},
		{store.ServiceRabbitMQ, svc, []string{`"username":"envoryx"`, `"webUiPort":15672`}},
		{store.ServiceMeilisearch, svc, []string{`"hostPort":6379`}},
		{store.ServiceTypesense, svc, []string{`"hostPort":6379`}},
		{store.ServiceOpenSearch, svc, nil},
		{store.ServiceOpenSearchDashboards, svc, nil},
		{store.ServiceMailpit, svc, nil},
		{store.ServiceMemcached, svc, nil},
		{store.ServiceOllama, `{"hostPort":11434,"gpu":true,"apiKey":"` + sealed + `"}`, []string{`"gpu":true`}},
		{store.ServiceStorage, `{"hostPort":9000,"consolePort":9001,"accessKey":"` + sealed + `","secretKey":"` + sealed + `","bucket":"shop","publicRead":true}`, []string{`"bucket":"shop"`, `"publicRead":true`, `"consolePort":9001`}},
		{store.ServiceDatabase, `{"rootPassword":"` + sealed + `","password":"` + sealed + `","username":"shop","database":"shop"}`, []string{`"username":"shop"`}},
		{store.DatabaseKind("analytics"), `{"rootPassword":"` + sealed + `","password":"` + sealed + `","username":"a"}`, []string{`"username":"a"`}},
		// A kind nobody has classified yet shows nothing.
		{store.ServiceKind("vault"), `{"token":"` + sealed + `","hostPort":8200}`, []string{`{}`}},
	}
	for _, c := range cases {
		got := string(redactedConfig(store.ProjectService{Kind: c.kind, Config: json.RawMessage(c.cfg)}))
		if strings.Contains(got, sealed) {
			t.Errorf("%s: secret in %s", c.kind, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %s lacks %s", c.kind, got, w)
			}
		}
	}

	// Runtime settings pass through untouched: the UI edits them as a whole.
	php := `{"memoryLimit":"512M","extensions":["intl"],"xdebug":true}`
	if got := string(redactedConfig(store.ProjectService{Kind: store.ServicePHP, Config: json.RawMessage(php)})); got != php {
		t.Errorf("php config changed: %s", got)
	}
}
