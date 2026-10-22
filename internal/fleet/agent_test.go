package fleet

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/plantest"
)

// fakeManager is the manager's side of the protocol: one token, one plan and one
// suspension to push.
type fakeManager struct {
	t     *testing.T
	token string

	mu   sync.Mutex
	key  ed25519.PublicKey
	plan string
	// suspension and certificate are sent after the plan unless empty.
	suspension  string
	certificate string
	// askSupport sends a support login request after the plan; replies holds the answers.
	askSupport bool
	replies    []message
	statuses   []Status
	revoked    bool
	conns      int
	enrolls    int
	conn       *websocket.Conn
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
		f.mu.Lock()
		f.enrolls++
		f.mu.Unlock()
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
		p, susp, cert := f.plan, f.suspension, f.certificate
		f.conn = conn
		f.mu.Unlock()
		_ = wsjson.Write(ctx, conn, message{Type: "plan", Plan: json.RawMessage(p)})
		if susp != "" {
			_ = wsjson.Write(ctx, conn, message{Type: "suspension", Suspension: json.RawMessage(susp)})
		}
		if cert != "" {
			_ = wsjson.Write(ctx, conn, message{Type: "certificate", Certificate: json.RawMessage(cert)})
		}
		if f.askSupport {
			_ = wsjson.Write(ctx, conn, message{Type: "supportLogin", ID: "r1", Name: "hoster"})
			_ = wsjson.Write(ctx, conn, message{Type: "backup", ID: "r2"})
		}
		for {
			var m message
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				return
			}
			f.mu.Lock()
			switch {
			case m.Type == "status" && m.Status != nil:
				f.statuses = append(f.statuses, *m.Status)
			case m.Type == "supportLogin" || m.Type == "backup":
				f.replies = append(f.replies, m)
			}
			f.mu.Unlock()
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
	f := &fakeManager{t: t, token: "tok", plan: `{"name":"Starter","limits":{"projects":2}}`,
		suspension: `{"since":"2026-10-11T08:00:00Z","message":"Invoice 1234 is open"}`}
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
	waitFor(t, "the suspension reported", func() bool { s, _ := f.lastStatus(); return !s.SuspendedSince.IsZero() })
	if s := holder.Suspension(); s == nil || s.Message != "Invoice 1234 is open" {
		t.Errorf("suspension = %+v", s)
	}
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
	f.plan, f.suspension = "null", "null"
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
	waitFor(t, "the suspension lifted", func() bool { return holder.Suspension() == nil })
	if _, err := os.Stat(filepath.Join(dir, plan.SuspensionFile)); !os.IsNotExist(err) {
		t.Errorf("suspension file after lifting: %v", err)
	}
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

	// Added again with a new token: the instance enrolls again although its state file
	// names the same manager.
	f.mu.Lock()
	f.token, f.revoked, f.enrolls = "tok2", false, 0
	f.mu.Unlock()
	run := func(token string) (*Agent, func()) {
		a, err := New(Config{URL: srv.URL, Token: token, ConfigDir: dir, Plan: holder, Log: log})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()
		return a, func() { cancel(); <-done }
	}
	a, stop := run("tok2")
	waitFor(t, "connected after the new token", func() bool { return a.Info().Connected })
	stop()
	// The used token that stays set enrolls nothing on the next start.
	a, stop = run("tok2")
	waitFor(t, "connected with the used token", func() bool { return a.Info().Connected })
	stop()
	f.mu.Lock()
	enrolls := f.enrolls
	f.mu.Unlock()
	if enrolls != 1 {
		t.Errorf("%d enrollments, want 1", enrolls)
	}
}

// A state file from before the token hash, with the used token still set: the one try
// the manager refuses is noted, and the instance stays enrolled.
func TestAgentKeepsItsEnrollmentWhenTheTokenIsRefused(t *testing.T) {
	f := &fakeManager{t: t, token: "tok", plan: `{"name":"Starter"}`}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := t.TempDir()
	holder, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub, priv, _ := ed25519.GenerateKey(nil)
	f.key, f.token = pub, ""
	b, _ := json.Marshal(state{URL: srv.URL, ID: "inst-1", Key: priv.Seed()})
	if err := os.WriteFile(filepath.Join(dir, StateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		a, _ := New(Config{URL: srv.URL, Token: "tok", ConfigDir: dir, Plan: holder, Log: log})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()
		waitFor(t, "connected with the old key", func() bool { return a.Info().Connected })
		cancel()
		<-done
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enrolls != 1 {
		t.Errorf("%d enrollments, want 1", f.enrolls)
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

func TestAgentAnswersSupportLoginsAndBackups(t *testing.T) {
	f := &fakeManager{t: t, token: "tok", plan: "null", askSupport: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := t.TempDir()
	holder, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil {
		t.Fatal(err)
	}
	var asked string
	started := make(chan struct{}, 1)
	a, _ := New(Config{URL: srv.URL, Token: "tok", ConfigDir: dir, Plan: holder, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SupportLogin: func(_ context.Context, hoster string) (string, error) {
			asked = hoster
			return "https://envoryx.c1.example.net/api/v1/auth/support/abc", nil
		},
		Backup: func(context.Context) error { started <- struct{}{}; return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "the answers", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.replies) == 2 })
	<-started
	f.mu.Lock()
	r := f.replies[0]
	if r.Type != "supportLogin" {
		r = f.replies[1]
	}
	f.mu.Unlock()
	if r.ID != "r1" || r.URL != "https://envoryx.c1.example.net/api/v1/auth/support/abc" || r.Error != "" || asked != "hoster" {
		t.Fatalf("reply = %+v, asked %q", r, asked)
	}
}

func TestAgentWantsHTTPS(t *testing.T) {
	for raw, want := range map[string]string{
		"https://fleet.example.net":     "ok",
		"http://localhost:8080":         "ok",
		"http://127.0.0.1:8080":         "ok",
		"http://127.1.2.3":              "ok",
		"http://[::1]:8080":             "ok",
		"http://fleet.example.net":      "refused",
		"http://10.0.0.5:8080":          "refused",
		"http://localhost.evil.example": "refused",
		"ftp://fleet.example.net":       "refused",
		"https://":                      "refused",
	} {
		warn, err := checkURL(raw, false)
		if got := map[bool]string{true: "refused", false: "ok"}[err != nil]; got != want || warn {
			t.Errorf("%s: %v, warn %v", raw, err, warn)
		}
	}
	// ENVORYX_FLEET_INSECURE lets plain http through, with a warning; nothing else.
	if warn, err := checkURL("http://fleet.example.net", true); err != nil || !warn {
		t.Errorf("insecure: %v, warn %v", err, warn)
	}
	if _, err := checkURL("ftp://fleet.example.net", true); err == nil {
		t.Error("insecure let ftp through")
	}
	if warn, _ := checkURL("https://fleet.example.net", true); warn {
		t.Error("a warning for https")
	}

	if _, err := New(Config{URL: "http://fleet.example.net", ConfigDir: t.TempDir()}); err == nil {
		t.Error("New took plain http")
	}
	if a, err := New(Config{URL: "http://fleet.example.net", Insecure: true, ConfigDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err != nil || a == nil {
		t.Errorf("New with ENVORYX_FLEET_INSECURE: %v", err)
	}
	// An instance enrolled at plain http by an older version doesn't keep dialing it.
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(nil)
	b, _ := json.Marshal(state{URL: "http://fleet.example.net", ID: "inst-1", Key: priv.Seed()})
	if err := os.WriteFile(filepath.Join(dir, StateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{ConfigDir: dir}); err == nil {
		t.Error("New took the enrolled plain http address")
	}
	if _, err := New(Config{URL: "https://other.example.net", ConfigDir: dir}); err == nil {
		t.Error("New took the enrolled plain http address without a token for the new manager")
	}
	// A token for another manager replaces the enrollment.
	if _, err := New(Config{URL: "https://other.example.net", Token: "tok", ConfigDir: dir}); err != nil {
		t.Errorf("a new manager with a token: %v", err)
	}
}

func TestReconnectDelay(t *testing.T) {
	// A connection that held for a while: back at once.
	if d := reconnectDelay(time.Minute, 10*time.Minute); d != time.Second {
		t.Errorf("after a long connection: %v", d)
	}
	// One the manager dropped right away: never under minReconnect, and the growth so far
	// is kept.
	if d := reconnectDelay(time.Second, 100*time.Millisecond); d != minReconnect {
		t.Errorf("after a quick drop: %v", d)
	}
	if d := reconnectDelay(40*time.Second, 100*time.Millisecond); d != 40*time.Second {
		t.Errorf("after quick drops in a row: %v", d)
	}
}

func TestClipName(t *testing.T) {
	long := strings.Repeat("ä", 1<<20)
	if got := clipName(long); len([]rune(got)) != maxHosterName {
		t.Errorf("clipped to %d characters", len([]rune(got)))
	}
	if got := clipName("  Jane Doe "); got != "Jane Doe" {
		t.Errorf("clipName = %q", got)
	}
}

// A manager that floods the instance with requests gets one answered at a time per kind;
// the rest hear "busy" at once instead of piling up.
func TestAgentAnswersOneRequestOfAKindAtATime(t *testing.T) {
	got := make(chan message, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			var m message
			if err := wsjson.Read(r.Context(), conn, &m); err != nil {
				return
			}
			got <- m
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	a, err := New(Config{URL: "https://fleet.example.net", ConfigDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var calls sync.WaitGroup
	var n int
	var mu sync.Mutex
	handle := func(_ context.Context, c *websocket.Conn, req message) {
		mu.Lock()
		n++
		mu.Unlock()
		<-release
		_ = wsjson.Write(ctx, c, message{Type: req.Type, ID: req.ID})
		calls.Done()
	}
	calls.Add(2)
	a.answer(ctx, conn, message{Type: "update", ID: "u1"}, handle)
	for i := range 3 {
		a.answer(ctx, conn, message{Type: "update", ID: fmt.Sprintf("u%d", i+2)}, handle)
	}
	// Another kind is not held up by it.
	a.answer(ctx, conn, message{Type: "backup", ID: "b1"}, handle)
	for range 3 {
		select {
		case m := <-got:
			if m.Type != "update" || m.Error == "" {
				t.Fatalf("expected busy, got %+v", m)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no busy answer")
		}
	}
	close(release)
	calls.Wait()
	mu.Lock()
	if n != 2 {
		t.Errorf("%d requests handled, want 2", n)
	}
	mu.Unlock()
	// Once answered, the next request of the kind is taken again.
	waitFor(t, "the update to be done", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.busy["update"] })
	calls.Add(1)
	a.answer(ctx, conn, message{Type: "update", ID: "u9"}, handle)
	calls.Wait()
}

// The hoster's certificate lands in its file, readable by the owner only; one that doesn't
// fit is refused and reported, and null takes the certificate back.
func TestAgentTakesTheCertificate(t *testing.T) {
	cert := plantest.Certificate(t, "kunde.envbay.dev")
	f := &fakeManager{t: t, token: "tok", plan: "null", certificate: plantest.JSON(t, cert)}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	dir := t.TempDir()
	holder, err := plan.Open(filepath.Join(dir, plan.File), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{URL: srv.URL, Token: "tok", ConfigDir: dir, Plan: holder, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	run := func() func() {
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.Run(ctx); close(done) }()
		return func() { cancel(); <-done }
	}
	stop := run()
	waitFor(t, "the certificate", func() bool { c := holder.Certificate(); return c != nil && c.Domain == "kunde.envbay.dev" })
	if fi, err := os.Stat(filepath.Join(dir, plan.CertificateFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("certificate file: %v %v", fi, err)
	}
	stop()

	cfg.Token = ""
	other := plantest.Certificate(t, "other.envbay.dev")
	f.mu.Lock()
	f.certificate = plantest.JSON(t, plan.Certificate{Domain: "kunde.envbay.dev", Certificate: other.Certificate, Key: other.Key})
	f.mu.Unlock()
	stop = run()
	waitFor(t, "the refusal reported", func() bool {
		s, _ := f.lastStatus()
		return s.Certificate != nil && strings.Contains(s.Certificate.Error, "doesn't cover")
	})
	if c := holder.Certificate(); c == nil || c.Certificate != cert.Certificate {
		t.Fatal("a refused certificate replaced the one in use")
	}
	stop()

	f.mu.Lock()
	f.certificate = "null"
	f.mu.Unlock()
	stop = run()
	waitFor(t, "the certificate taken back", func() bool { return holder.Certificate() == nil })
	stop()
}
