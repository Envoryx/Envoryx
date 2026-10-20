package project

import (
	"context"
	"testing"
	"time"
)

// A start reports back once the database passes its health check, not while it still
// initialises.
func TestStartWaitsForTheDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", false)
	req.Database = &DatabaseRequest{Type: "mariadb", Version: "11"}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !e.engine.SetHealth("envoryx-shop-database", "starting", "") {
		t.Fatal("no database container")
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		e.engine.SetHealth("envoryx-shop-database", "healthy", "")
	}()
	begin := time.Now()
	if _, err := e.m.Start(ctx, v.Project.ID); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d < 700*time.Millisecond {
		t.Fatalf("the start returned after %v, before the database was ready", d)
	}
}
