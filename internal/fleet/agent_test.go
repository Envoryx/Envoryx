package fleet

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/envoryx/envoryx/internal/plan"
)

// fakeManager is the manager's side of the protocol: one token, one plan to push.
type fakeManager struct {
	t     *testing.T
	token string

	mu       sync.Mutex
	key      ed25519.PublicKey
	plan     string
	statuses []Status
	revoked  bool
	conns    int
	conn     *websocket.Conn
}

// revoke removes the instance as the manager does: the open connection is closed with
// CloseRevoked and new ones are refused.
func (f *fakeManager) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = true
	if f.conn != nil {
		_ = f.conn.Close(CloseRevoked, "removed")
	}
}

func (f *fakeManager) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+enrollPath, func(w http.ResponseWriter, r *http.Request) {
		var req enrollRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Token != f.token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_token","message":"the token is unknown or used"}}`)
			return
		}
		f.mu.Lock()
		f.key, f.token = req.PublicKey, ""
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(enrollResponse{ID: "inst-1", Name: "Kunde 1"})
	})
	mux.HandleFunc("GET "+connectPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderInstance) != "inst-1" {
			http.Error(w, "unknown", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		revoked := f.revoked
		f.conns++
		f.mu.Unlock()
		if revoked {
			http.Error(w, "removed", http.StatusGone)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		nonce := []byte("0123456789abcdef0123456789abcdef")
		_ = wsjson.Write(ctx, conn, message{Type: "challenge", Nonce: nonce})
		var auth message
		if err := wsjson.Read(ctx, conn, &auth); err != nil {
			return
		}
		f.mu.Lock()
		key := f.key
		f.mu.Unlock()
		if !ed25519.Verify(key, challengeText(nonce), auth.Signature) {
			conn.Close(websocket.StatusPolicyViolation, "bad signature")
			return
		}
		_ = wsjson.Write(ctx, conn, message{Type: "welcome", Name: "Kunde 1"})
		f.mu.Lock()
		p := f.plan
		f.conn = conn
		f.mu.Unlock()
		_ = wsjson.Write(ctx, conn, message{Type: "plan", Plan: json.RawMessage(p)})
		for {
			var m message
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				return
			}
			if m.Type == "status" && m.Status != nil {
				f.mu.Lock()
				f.statuses = append(f.statuses, *m.Status)
				f.mu.Unlock()
			}
		}
	})
	return mux
}

func (f *fakeManager) lastStatus() (Status, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return Status{}, 0
	}
	return f.statuses[len(f.statuses)-1], len(f.statuses)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentEnrollsTakesThePlanAndReports(t *testing.T) {
	f := &fakeManager{t: t, token: "tok", plan: `{"name":"Starter","limits":{"projects":2}}`}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := t.TempDir()
	holder, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{URL: srv.URL, Token: "tok", ConfigDir: dir, Plan: holder, Log: log,
		Status: func(context.Context) Status {
			return Status{Version: "v0.28.0", InstanceID: "abc", Usage: Usage{Projects: 1}}
		}}
	a, err := New(cfg)
	if err != nil || a == nil {
		t.Fatalf("New: %v %v", a, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	waitFor(t, "the plan", func() bool { p := holder.Get(); return p != nil && p.Name == "Starter" })
	waitFor(t, "a status with the plan", func() bool { s, _ := f.lastStatus(); return s.PlanName == "Starter" })
	if s, _ := f.lastStatus(); s.Version != "v0.28.0" || s.Usage.Projects != 1 {
		t.Errorf("status = %+v", s)
	}
	if in := a.Info(); !in.Connected || in.Name != "Kunde 1" {
		t.Errorf("info = %+v", in)
	}
	cancel()
	<-done

	// The enrollment is kept: a restart connects without the token, which is used up.
	if fi, err := os.Stat(filepath.Join(dir, StateFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
	f.mu.Lock()
	f.plan = "null"
	f.mu.Unlock()
	cfg.Token = ""
	a, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	waitFor(t, "the plan removed", func() bool { return holder.Get() == nil })
	cancel()
	<-done
}

func TestAgentRefusesABrokenPlanAndStopsWhenRemoved(t *testing.T) {
	f := &fakeManager{t: t, token: "tok", plan: `{"limits":{"projects":-1}}`}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, plan.File), []byte(`{"name":"Old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, _ := New(Config{URL: srv.URL, Token: "tok", ConfigDir: dir, Plan: holder, Log: log})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	waitFor(t, "the plan error reported", func() bool { s, _ := f.lastStatus(); return s.PlanError != "" })
	if p := holder.Get(); p == nil || p.Name != "Old" {
		t.Errorf("the old plan must stay: %+v", p)
	}

	// Removed from the fleet: the next connection is refused and the agent stops.
	f.revoke()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent kept running after it was removed")
	}
	if in := a.Info(); in.Error == "" {
		t.Error("no error after removal")
	}
}

func TestAgentWithoutFleet(t *testing.T) {
	a, err := New(Config{ConfigDir: t.TempDir()})
	if err != nil || a != nil {
		t.Fatalf("New without URL or state: %v %v", a, err)
	}
	if a.Info() != nil {
		t.Error("nil agent has info")
	}
	if _, err := New(Config{URL: "ftp://x", ConfigDir: t.TempDir()}); err == nil {
		t.Error("ftp URL accepted")
	}
}
