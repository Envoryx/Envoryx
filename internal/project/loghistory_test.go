package project

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestLogHistoryOutlivesTheContainer(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls, err := logs.OpenStore(filepath.Join(e.cfgDir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	e.m.SetLogStore(ls)
	view, err := e.m.Create(ctx, phpRequest("Hist", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	base := time.Now().Add(-time.Hour).UTC()
	e.engine.Logs["envoryx-hist-php"] = []docker.LogLine{
		{Time: base, Stream: "stderr", Text: "NOTICE: fpm is running"},
		{Time: base.Add(time.Second), Stream: "stderr", Text: "PHP Fatal error: first container"},
	}

	// Before the collector ran, queries read the container.
	page, err := e.m.QueryLogs(ctx, id, store.ServicePHP, logs.Query{}, 10)
	if err != nil || page.Source != LogSourceContainer || len(page.Lines) != 2 {
		t.Fatalf("container source: %+v %v", page, err)
	}

	go e.m.RunLogHistory(ctx, 10*time.Millisecond, e.m.log)
	k := logs.Key{Project: id, Service: "php"}
	deadline := time.Now().Add(3 * time.Second)
	for ls.Last(k).Before(base.Add(time.Second)) {
		if time.Now().After(deadline) {
			t.Fatal("the collector did not store the output")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Recreating the container resets Docker's log; the history keeps the old lines.
	if err := e.m.recreateContainers(ctx, id, store.ServicePHP); err != nil {
		t.Fatal(err)
	}
	e.engine.SetLogs("envoryx-hist-php", docker.LogLine{Time: base.Add(time.Minute), Stream: "stderr", Text: "NOTICE: second container"})
	deadline = time.Now().Add(3 * time.Second)
	for ls.Last(k).Before(base.Add(time.Minute)) {
		if time.Now().After(deadline) {
			t.Fatal("the recreated container was not collected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err = e.m.QueryLogs(ctx, id, store.ServicePHP, logs.Query{}, 10)
	if err != nil || page.Source != LogSourceHistory || page.Matched != 3 || page.Lines[1].Level != "error" || !page.Oldest.Equal(base) {
		t.Fatalf("history: %+v %v", page, err)
	}
	page, _ = e.m.QueryLogs(ctx, id, store.ServicePHP, logs.Query{MinLevel: logs.LevelError, Since: base.Add(-time.Minute)}, 10)
	if page.Matched != 1 || page.Lines[0].Text != "PHP Fatal error: first container" {
		t.Fatalf("filtered history: %+v", page)
	}
	sum, err := e.m.LogStats(ctx, id, store.ServicePHP, logs.Query{Since: base.Add(-time.Minute)})
	if err != nil || sum.Total != 3 || sum.Errors != 1 {
		t.Fatalf("stats: %+v %v", sum, err)
	}

	// Switched off, queries go back to the container.
	off := false
	if _, err := e.m.SetLogHistory(ctx, LogHistoryUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if page, _ := e.m.QueryLogs(ctx, id, store.ServicePHP, logs.Query{}, 10); page.Source != LogSourceContainer || page.Matched != 1 {
		t.Fatalf("history off: %+v", page)
	}
	info := e.m.LogHistoryInfo(ctx)
	if info.Enabled || !info.Available || info.Usage.Files == 0 || info.RetentionDays != DefaultLogHistoryDays {
		t.Fatalf("info: %+v", info)
	}

	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "hist"}); err != nil {
		t.Fatal(err)
	}
	if ls.Has(k) {
		t.Fatal("deleting the project must delete its history")
	}
}

func TestLogHistorySettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if s := e.m.LogHistory(ctx); !s.Enabled || s.RetentionDays != 7 || s.MaxMB != 1024 {
		t.Fatalf("defaults: %+v", s)
	}
	days, mb := 30, 200
	s, err := e.m.SetLogHistory(ctx, LogHistoryUpdate{RetentionDays: &days, MaxMB: &mb})
	if err != nil || s.RetentionDays != 30 || s.MaxMB != 200 || !s.Enabled {
		t.Fatalf("set: %+v %v", s, err)
	}
	for _, upd := range []LogHistoryUpdate{{RetentionDays: new(0)}, {RetentionDays: new(366)}, {MaxMB: new(10)}} {
		if _, err := e.m.SetLogHistory(ctx, upd); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%+v must be refused: %v", upd, err)
		}
	}
	if err := e.m.ClearLogHistory(ctx); err == nil {
		t.Fatal("clearing without a store must fail")
	}
	for kind, want := range map[string]bool{"php": true, "opensearch-dashboards": true, "worker:3f0b4a9e-1a2b-4c3d-8e9f-0a1b2c3d4e5f": true, "git": false, "template": false, "worker:x": false, "": false} {
		if IsLogService(kind) != want {
			t.Errorf("IsLogService(%q) != %v", kind, want)
		}
	}
}
