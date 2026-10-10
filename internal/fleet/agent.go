package fleet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/envoryx/envoryx/internal/plan"
)

// StateFile holds the instance's place in the fleet in the config directory: the
// manager's address, the instance's fleet ID and its private key. Like the secret key it
// stays out of instance backups.
const StateFile = "fleet.json"

// statusInterval is how often a connected instance reports its state.
const statusInterval = time.Minute

// state is the content of StateFile.
type state struct {
	URL  string `json:"url"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Key is the Ed25519 seed of the instance's key.
	Key []byte `json:"key"`
}

// Config wires an Agent.
type Config struct {
	// URL and Token come from ENVORYX_FLEET_URL and ENVORYX_FLEET_TOKEN. The token is only
	// needed until the instance is enrolled.
	URL, Token string
	// ConfigDir holds StateFile and the plan file.
	ConfigDir string
	Plan      *plan.Holder
	// Status reports the instance's state for the manager.
	Status func(ctx context.Context) Status
	Log    *slog.Logger
	// HTTPClient enrolls (http.DefaultClient when nil).
	HTTPClient *http.Client
}

// Info is the connection as the instance's UI shows it.
type Info struct {
	URL         string    `json:"url"`
	Name        string    `json:"name,omitempty"`
	Connected   bool      `json:"connected"`
	LastContact time.Time `json:"lastContact,omitzero"`
	Error       string    `json:"error,omitempty"`
}

// Agent keeps an instance connected to its fleet manager.
type Agent struct {
	cfg Config

	mu        sync.Mutex
	st        *state
	connected bool
	contact   time.Time
	err       string
	planErr   string
}

// New returns the agent of an instance that is or shall be managed: one with
// ENVORYX_FLEET_URL or a state file. Otherwise it returns nil.
func New(cfg Config) (*Agent, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	a := &Agent{cfg: cfg}
	st, err := readState(filepath.Join(cfg.ConfigDir, StateFile))
	if err != nil {
		return nil, err
	}
	a.st = st
	if cfg.URL == "" && st == nil {
		return nil, nil
	}
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("ENVORYX_FLEET_URL must be an http(s) address, got %q", cfg.URL)
		}
	}
	return a, nil
}

func readState(path string) (*state, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil || st.ID == "" || st.URL == "" || len(st.Key) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is damaged; remove it and enroll again with a new token", path)
	}
	return &st, nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Info returns the state of the connection.
func (a *Agent) Info() *Info {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	in := &Info{URL: a.cfg.URL, Connected: a.connected, LastContact: a.contact, Error: a.err}
	if a.st != nil {
		in.URL, in.Name = a.st.URL, a.st.Name
	}
	return in
}

func (a *Agent) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.err = ""
		return
	}
	if a.err != err.Error() {
		a.cfg.Log.Warn("fleet: "+err.Error(), "url", a.cfg.URL)
	}
	a.err = err.Error()
}

// errRevoked ends Run: the manager removed the instance.
var errRevoked = errors.New("the fleet manager removed this instance; it keeps its last plan")

// Run enrolls the instance when needed and keeps it connected until ctx ends or the
// manager removes it.
func (a *Agent) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := a.runOnce(ctx)
		if errors.Is(err, errRevoked) {
			a.setErr(err)
			return
		}
		if ctx.Err() != nil {
			return
		}
		a.setErr(err)
		if a.Info().LastContact.After(time.Now().Add(-time.Minute)) {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

func (a *Agent) runOnce(ctx context.Context) error {
	st, err := a.ensureEnrolled(ctx)
	if err != nil {
		return err
	}
	return a.connect(ctx, st)
}

// ensureEnrolled returns the instance's state, enrolling with the token first when the
// instance has none or ENVORYX_FLEET_URL points at another manager.
func (a *Agent) ensureEnrolled(ctx context.Context) (*state, error) {
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	if st != nil && (a.cfg.URL == "" || sameManager(st.URL, a.cfg.URL)) {
		return st, nil
	}
	if a.cfg.Token == "" {
		if st != nil {
			// Another manager is configured but no token to join it: stay with the old one.
			return st, nil
		}
		return nil, errors.New("set ENVORYX_FLEET_TOKEN to the enrollment token from the fleet manager")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	var status Status
	if a.cfg.Status != nil {
		status = a.cfg.Status(ctx)
	}
	body, _ := json.Marshal(enrollRequest{Token: a.cfg.Token, PublicKey: pub, InstanceID: status.InstanceID, Version: status.Version, Hostname: host})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.URL, "/")+enrollPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enrolling at the fleet manager: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the fleet manager refused the enrollment (HTTP %d): %s", res.StatusCode, apiMessage(raw))
	}
	var out enrollResponse
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return nil, errors.New("the fleet manager's answer to the enrollment is unreadable")
	}
	st = &state{URL: strings.TrimRight(a.cfg.URL, "/"), ID: out.ID, Name: out.Name, Key: priv.Seed()}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := writeFileAtomic(filepath.Join(a.cfg.ConfigDir, StateFile), b); err != nil {
		return nil, fmt.Errorf("saving the fleet enrollment: %w", err)
	}
	a.mu.Lock()
	a.st = st
	a.mu.Unlock()
	a.cfg.Log.Info("fleet: enrolled", "url", st.URL, "id", st.ID, "name", st.Name)
	return st, nil
}

func sameManager(a, b string) bool { return strings.TrimRight(a, "/") == strings.TrimRight(b, "/") }

// apiMessage is the message of an error answer, or its start.
func apiMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func wsURL(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + connectPath
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + connectPath
	}
	return base + connectPath
}

// connect holds one connection: it proves the key, takes the plans the manager sends and
// reports the state every minute.
func (a *Agent) connect(ctx context.Context, st *state) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, res, err := websocket.Dial(dialCtx, wsURL(st.URL), &websocket.DialOptions{
		HTTPClient: a.cfg.HTTPClient,
		HTTPHeader: http.Header{HeaderInstance: []string{st.ID}},
	})
	if err != nil {
		if res != nil && res.StatusCode == http.StatusGone {
			return errRevoked
		}
		return fmt.Errorf("connecting to the fleet manager: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	key := ed25519.NewKeyFromSeed(st.Key)

	var hello message
	if err := wsjson.Read(dialCtx, conn, &hello); err != nil || hello.Type != "challenge" || len(hello.Nonce) < 16 {
		return closeErr(err, "the fleet manager sent no challenge")
	}
	if err := wsjson.Write(dialCtx, conn, message{Type: "auth", Signature: signChallenge(key, hello.Nonce)}); err != nil {
		return closeErr(err, "")
	}
	var welcome message
	if err := wsjson.Read(dialCtx, conn, &welcome); err != nil || welcome.Type != "welcome" {
		return closeErr(err, "the fleet manager did not accept the instance")
	}
	cancel()
	a.mu.Lock()
	a.connected, a.contact, a.err = true, time.Now(), ""
	if welcome.Name != "" && st.Name != welcome.Name {
		st.Name = welcome.Name
		if b, err := json.MarshalIndent(st, "", "  "); err == nil {
			_ = writeFileAtomic(filepath.Join(a.cfg.ConfigDir, StateFile), b)
		}
	}
	a.mu.Unlock()
	a.cfg.Log.Info("fleet: connected", "url", st.URL, "name", st.Name)
	defer func() {
		a.mu.Lock()
		a.connected = false
		a.mu.Unlock()
	}()

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	sendStatus := func() error {
		s := a.status(ctx)
		wctx, c := context.WithTimeout(ctx, 30*time.Second)
		defer c()
		return wsjson.Write(wctx, conn, message{Type: "status", Status: &s})
	}
	if err := sendStatus(); err != nil {
		return closeErr(err, "")
	}
	readErr := make(chan error, 1)
	planned := make(chan struct{}, 1)
	go func() {
		for {
			var m message
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				readErr <- err
				return
			}
			a.mu.Lock()
			a.contact = time.Now()
			a.mu.Unlock()
			if m.Type == "plan" {
				a.applyPlan(m.Plan)
				select {
				case planned <- struct{}{}:
				default:
				}
			}
		}
	}()
	t := time.NewTicker(statusInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusGoingAway, "shutting down")
			return nil
		case err := <-readErr:
			return closeErr(err, "")
		case <-planned:
			// The manager sees right away whether the plan was taken over.
			if err := sendStatus(); err != nil {
				return closeErr(err, "")
			}
		case <-t.C:
			if err := sendStatus(); err != nil {
				return closeErr(err, "")
			}
		}
	}
}

// closeErr turns a broken connection into the agent's error.
func closeErr(err error, fallback string) error {
	if websocket.CloseStatus(err) == CloseRevoked {
		return errRevoked
	}
	if err == nil {
		return errors.New(fallback)
	}
	if fallback != "" {
		return fmt.Errorf("%s: %w", fallback, err)
	}
	return fmt.Errorf("fleet manager connection: %w", err)
}

func (a *Agent) status(ctx context.Context) Status {
	var s Status
	if a.cfg.Status != nil {
		s = a.cfg.Status(ctx)
	}
	if s.Hostname == "" {
		s.Hostname, _ = os.Hostname()
	}
	if p := a.cfg.Plan.Get(); p != nil {
		s.PlanName = p.Name
	}
	a.mu.Lock()
	s.PlanError = a.planErr
	a.mu.Unlock()
	if s.PlanError == "" {
		if err := a.cfg.Plan.Err(); err != nil {
			s.PlanError = err.Error()
		}
	}
	return s
}

// applyPlan writes the plan the manager sent (null removes it) and has it read.
func (a *Agent) applyPlan(raw json.RawMessage) {
	path := filepath.Join(a.cfg.ConfigDir, plan.File)
	var err error
	if len(raw) == 0 || string(raw) == "null" {
		if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else if _, err = plan.Parse(raw); err == nil {
		var buf bytes.Buffer
		if err = json.Indent(&buf, raw, "", "  "); err == nil {
			err = writeFileAtomic(path, buf.Bytes())
		}
	}
	a.mu.Lock()
	a.planErr = ""
	if err != nil {
		a.planErr = err.Error()
	}
	a.mu.Unlock()
	if err != nil {
		a.cfg.Log.Warn("fleet: plan not taken over", "err", err)
		return
	}
	a.cfg.Plan.Reload()
}
