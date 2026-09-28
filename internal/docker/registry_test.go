package docker

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

func TestRegistryAuth(t *testing.T) {
	for ref, want := range map[string]string{
		"php:8.4": "docker.io", "acme/php": "docker.io", "ghcr.io/acme/php:1": "ghcr.io",
		"registry.example.com:5000/php@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "registry.example.com:5000",
		"not a ref": "",
	} {
		if got := RegistryHost(ref); got != want {
			t.Errorf("%s: %q, want %q", ref, got, want)
		}
	}
	e := &MobyEngine{}
	if e.pullAuth("php:8.4") != "" || len(e.buildAuths()) != 0 {
		t.Fatal("no logins, no auth")
	}
	e.SetRegistryCredentials(func() []RegistryCredential {
		return []RegistryCredential{{Host: "ghcr.io", Username: "me", Password: "tok"}, {Host: "docker.io", Username: "hub", Password: "pw"}}
	})
	raw, err := base64.URLEncoding.DecodeString(e.pullAuth("ghcr.io/acme/php:1"))
	if err != nil {
		t.Fatal(err)
	}
	var ac registry.AuthConfig
	if json.Unmarshal(raw, &ac) != nil || ac.Username != "me" || ac.Password != "tok" {
		t.Fatalf("pull auth: %s", raw)
	}
	if e.pullAuth("quay.io/x/y") != "" {
		t.Fatal("a registry without a login pulls anonymously")
	}
	auths := e.buildAuths()
	if auths["ghcr.io"].Password != "tok" || auths[dockerHubAuthKey].Username != "hub" {
		t.Fatalf("build auths: %+v", auths)
	}
}
