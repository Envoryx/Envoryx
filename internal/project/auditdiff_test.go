package project

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/envoryx/envoryx/internal/manifest"
)

func TestManifestDiff(t *testing.T) {
	before := manifest.Manifest{
		Version: 1, Docroot: "public",
		PHP:     &manifest.PHP{Version: "8.3", MemoryLimit: "256M"},
		Env:     map[string]string{"APP_ENV": "local", "OLD": "x"},
		Domains: []string{"a.example.com", "b.example.com"},
		Workers: []manifest.Worker{{Name: "queue", Preset: "laravel:queue"}},
	}
	after := manifest.Manifest{
		Version: 1, Docroot: "web",
		PHP:     &manifest.PHP{Version: "8.4", MemoryLimit: "256M"},
		Redis:   &manifest.Service{},
		Env:     map[string]string{"APP_ENV": "production", "NEW": "y"},
		Domains: []string{"b.example.com"},
		Workers: []manifest.Worker{{Name: "queue", Preset: "laravel:queue"}},
	}
	got := manifestDiff(before, after)
	want := []AuditChange{
		{Section: "docroot", From: "public", To: "web"},
		{Section: "php", From: "version: 8.3", To: "version: 8.4"},
		{Section: "redis", To: "on"},
		{Section: "domains", Item: "a.example.com", From: "a.example.com"},
		{Section: "env", Item: "APP_ENV", From: "local", To: "production"},
		{Section: "env", Item: "NEW", To: "y"},
		{Section: "env", Item: "OLD", From: "x"},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("diff:\n%s", gj)
	}
	if d := manifestDiff(before, before); len(d) != 0 {
		t.Fatalf("no change, no diff: %+v", d)
	}
}

// An update records every setting it changed with its value before and after.
func TestUpdateAuditsBeforeAndAfter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Audit Diff", false))
	if err != nil {
		t.Fatal(err)
	}
	docroot := "web"
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Docroot: &docroot, Redis: &ExtraUpdate{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	entries, err := e.store.Audit.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var diff []AuditChange
	for _, en := range entries {
		if en.Action == "project.updated" && en.TargetID == view.Project.ID {
			var d struct{ Diff []AuditChange }
			_ = json.Unmarshal(en.Details, &d)
			diff = d.Diff
			break
		}
	}
	want := []AuditChange{{Section: "docroot", From: "public", To: "web"}, {Section: "redis", To: "version: 8"}}
	if !reflect.DeepEqual(diff, want) {
		t.Fatalf("audit diff: %+v", diff)
	}
}
