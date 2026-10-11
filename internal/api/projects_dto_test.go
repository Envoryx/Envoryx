package api

import (
	"encoding/json"
	"testing"
)

// A create or update that leaves the PHP settings out gets the development defaults,
// display_errors on included, like the wizard; what is given still counts.
func TestPHPRequestDefaults(t *testing.T) {
	var c createProjectRequest
	if err := json.Unmarshal([]byte(`{"name":"x","php":{"version":"8.4"}}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.PHP.Config.DisplayErrors || c.PHP.Config.MemoryLimit != "256M" || c.PHP.Version != "8.4" {
		t.Fatalf("create: %+v", c.PHP)
	}
	var off phpRequestDTO
	if err := json.Unmarshal([]byte(`{"config":{"displayErrors":false,"extensions":["gd"]}}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.Config.DisplayErrors || len(off.Config.Extensions) != 1 {
		t.Fatalf("given values: %+v", off.Config)
	}
	var u phpUpdateDTO
	if err := json.Unmarshal([]byte(`{"enabled":false,"version":"8.5"}`), &u); err != nil {
		t.Fatal(err)
	}
	if u.Enabled == nil || *u.Enabled || u.Version != "8.5" || !u.Config.DisplayErrors {
		t.Fatalf("update: %+v", u)
	}
}
