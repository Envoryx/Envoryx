package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/store"
)

func TestAuditQuery(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	add := func(at time.Time, user, action, target, details string) {
		t.Helper()
		if err := st.Audit.Append(ctx, store.AuditEntry{CreatedAt: at, Username: user, Action: action, TargetType: "project", TargetID: target, Details: json.RawMessage(details)}); err != nil {
			t.Fatal(err)
		}
	}
	add(base, "admin", "auth.login", "", `{}`)
	add(base.Add(time.Hour), "admin (token: ci)", "project.started", "p1", `{"name":"Shop"}`)
	add(base.Add(2*time.Hour), "dev", "project.updated", "p2", `{"name":"Blog 100%_off"}`)
	add(base.Add(3*time.Hour), "", "project.failed", "p1", `{"name":"Shop"}`)
	add(base.Add(24*time.Hour), "dev", "database.created", "p2", `{"database":"reports"}`)

	ids := func(q store.AuditQuery) []string {
		t.Helper()
		out, _, err := st.Audit.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range out {
			got = append(got, e.Action)
		}
		return got
	}
	for name, c := range map[string]struct {
		q    store.AuditQuery
		want []string
	}{
		"all newest first":       {store.AuditQuery{}, []string{"database.created", "project.failed", "project.updated", "project.started", "auth.login"}},
		"a user and its tokens":  {store.AuditQuery{User: "admin"}, []string{"project.started", "auth.login"}},
		"a project":              {store.AuditQuery{TargetID: "p1"}, []string{"project.failed", "project.started"}},
		"categories":             {store.AuditQuery{Actions: []string{"auth.", "database."}}, []string{"database.created", "auth.login"}},
		"text in details":        {store.AuditQuery{Text: "REPORTS"}, []string{"database.created"}},
		"percent is no wildcard": {store.AuditQuery{Text: "100%_"}, []string{"project.updated"}},
		"underscore literal":     {store.AuditQuery{Text: "d_c"}, nil},
		"time range":             {store.AuditQuery{Since: base.Add(time.Hour), Until: base.Add(3 * time.Hour)}, []string{"project.updated", "project.started"}},
		"combined":               {store.AuditQuery{User: "dev", Actions: []string{"project."}}, []string{"project.updated"}},
	} {
		if got := ids(c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}

	users, err := st.Audit.Users(ctx)
	if err != nil || !slices.Equal(users, []string{"admin", "dev"}) {
		t.Fatalf("users: %v %v", users, err)
	}
	n, err := st.Audit.Prune(ctx, base.Add(90*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if got := ids(store.AuditQuery{}); len(got) != 3 {
		t.Fatalf("after prune: %v", got)
	}
}

// Paging walks every entry exactly once, also when many share a timestamp.
func TestAuditPaging(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range 25 {
		// Five entries per second, some within the same instant.
		if err := st.Audit.Append(ctx, store.AuditEntry{CreatedAt: at.Add(time.Duration(i/5) * time.Second), Action: fmt.Sprintf("test.%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after, pages := "", 0
	for {
		page, next, err := st.Audit.Query(ctx, store.AuditQuery{Limit: 7, After: after})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range page {
			if seen[e.Action] {
				t.Fatalf("%s twice", e.Action)
			}
			seen[e.Action] = true
		}
		if next == "" {
			break
		}
		after = next
	}
	if len(seen) != 25 || pages != 4 {
		t.Fatalf("saw %d entries in %d pages", len(seen), pages)
	}
	count := 0
	if err := st.Audit.Each(ctx, store.AuditQuery{Actions: []string{"test.0"}}, func(store.AuditEntry) error { count++; return nil }); err != nil || count != 10 {
		t.Fatalf("each: %d %v", count, err)
	}
}

func TestAuditTargetNames(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := &store.Project{Name: "Blog", Slug: "blog", Path: "blog"}
	if err := st.Projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users.Create(ctx, "mia", "hash", "developer")
	if err != nil {
		t.Fatal(err)
	}
	details := func(e store.AuditEntry) map[string]any {
		t.Helper()
		var d map[string]any
		if err := json.Unmarshal(e.Details, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, e := range []store.AuditEntry{
		{Action: "terminal.opened", TargetType: "project", TargetID: p.ID, Details: json.RawMessage(`{"service":"php"}`)},
		{Action: "auth.login", TargetType: "user", TargetID: u.ID},
		{Action: "project.renamed", TargetType: "project", TargetID: p.ID, Details: json.RawMessage(`{"name":"Old blog"}`)},
		{Action: "project.started", TargetType: "project", TargetID: "gone"},
	} {
		if err := st.Audit.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := st.Audit.Query(ctx, store.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	byAction := map[string]map[string]any{}
	for _, e := range got {
		byAction[e.Action] = details(e)
	}
	if d := byAction["terminal.opened"]; d["name"] != "Blog" || d["service"] != "php" {
		t.Errorf("terminal entry: %v", d)
	}
	if d := byAction["auth.login"]; d["username"] != "mia" {
		t.Errorf("login entry: %v", d)
	}
	if d := byAction["project.renamed"]; d["name"] != "Old blog" {
		t.Errorf("a stored name must win: %v", d)
	}
	if d := byAction["project.started"]; len(d) != 0 {
		t.Errorf("unknown target got a name: %v", d)
	}

	// Entries written before names were stored get the current name when read.
	old := []store.AuditEntry{{Action: "test.run", TargetType: "project", TargetID: p.ID, Details: json.RawMessage(`{}`)}}
	st.Audit.NameTargets(ctx, old)
	if d := details(old[0]); d["name"] != "Blog" {
		t.Errorf("old entry: %v", d)
	}
}
