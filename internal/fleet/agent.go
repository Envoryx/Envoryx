package fleet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

// stableConnection is how long a connection must have held for Run to reconnect at once
// after it broke; minReconnect is the least Run waits after a shorter one.
const (
	stableConnection = time.Minute
	minReconnect     = 5 * time.Second
)

// maxHosterName is how much of the name of the hoster's user who asks for a support
// login is kept (log and audit log).
const maxHosterName = 100

// suspensionReport is when the state goes out again after a suspension: once the
// projects are stopped.
const suspensionReport = 10 * time.Second

// state is the content of StateFile.
type state struct {
	URL  string `json:"url"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Key is the Ed25519 seed of the instance's key.
	Key []byte `json:"key"`
	// TokenHash is the hash of the last enrollment token the instance used or tried: a
	// token in ENVORYX_FLEET_TOKEN with another hash is new and enrolls the instance
	// again (after it was removed, or with a new key), one with this hash is left alone.
	TokenHash string `json:"tokenHash,omitempty"`
}

// tokenHash is the hash of an enrollment token as the state keeps it.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Config wires an Agent.
type Config struct {
	// URL and Token come from ENVORYX_FLEET_URL and ENVORYX_FLEET_TOKEN. The token is only
	// needed until the instance is enrolled.
	URL, Token string
	// Insecure (ENVORYX_FLEET_INSECURE) allows a manager at a plain http address that is
	// not on this machine; see checkURL.
	Insecure bool
	// ConfigDir holds StateFile and the plan file.
	ConfigDir string
	Plan      *plan.Holder
	// Status reports the instance's state for the manager.
	Status func(ctx context.Context) Status
	// SupportLogin returns a one-time login link for the hoster's support, named hoster,
	// or why there is none (the admins don't allow it).
	SupportLogin func(ctx context.Context, hoster string) (string, error)
	// Backup starts an instance backup the manager asked for and returns at once; the
	// result shows in the status. It fails when one is running already.
	Backup func(ctx context.Context) error
	// Update starts the update to a version and returns once it began; the status tells
	// how it goes.
	Update func(ctx context.Context, version string) error
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
	// certErr is why the last certificate from the manager was not taken over.
	certErr string
	// nudge asks for a status report now (Nudge).
	nudge chan struct{}
	// busy holds the request types (supportLogin, backup, update) being answered: one of
	// each at a time, so a manager that floods the instance gets "busy", not a goroutine
	// per request.
	busy map[string]bool
	// connectedAt is when the last connection was accepted (Run's reconnect delay).
	connectedAt time.Time
}

// New returns the agent of an instance that is or shall be managed: one with
// ENVORYX_FLEET_URL or a state file. Otherwise it returns nil.
func New(cfg Config) (*Agent, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	a := &Agent{cfg: cfg, nudge: make(chan struct{}, 1), busy: map[string]bool{}}
	st, err := readState(filepath.Join(cfg.ConfigDir, StateFile))
	if err != nil {
		return nil, err
	}
	a.st = st
	if cfg.URL == "" && st == nil {
		return nil, nil
	}
	// Both addresses the instance may dial are checked: the configured one and the one it
	// enrolled at (which may stem from a version that took http). The latter stays in use
	// when ENVORYX_FLEET_URL is unset, or names another manager without a token to join it
	// (ensureEnrolled); otherwise it is ENVORYX_FLEET_URL, or gets replaced.
	enrolledURL := ""
	if st != nil && (cfg.URL == "" || (cfg.Token == "" && !sameManager(st.URL, cfg.URL))) {
		enrolledURL = st.URL
	}
	for _, c := range []struct{ addr, what string }{{cfg.URL, "ENVORYX_FLEET_URL"}, {enrolledURL, filepath.Join(cfg.ConfigDir, StateFile)}} {
		if c.addr == "" {
			continue
		}
		insecure, err := checkURL(c.addr, cfg.Insecure)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.what, err)
		}
		if insecure && cfg.Log != nil {
			cfg.Log.Warn("fleet: the manager is reached over plain http (ENVORYX_FLEET_INSECURE): anyone on the network between can pose as it", "url", c.addr)
		}
	}
	return a, nil
}

// checkURL checks the manager's address. Only TLS proves the manager to the instance:
// whoever answers at a plain http address gets to push plans (with an offsite target of
// its own), suspend and update the instance and ask for support logins. So it must be
// https; http only to this machine (localhost, 127.0.0.0/8, ::1) or with insecure set,
// which checkURL reports for a warning.
func checkURL(raw string, insecure bool) (warn bool, err error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false, fmt.Errorf("the fleet manager's address must be an https address, got %q", raw)
	}
	if u.Scheme == "https" || isLoopback(u.Hostname()) {
		return false, nil
	}
	if !insecure {
		return false, fmt.Errorf("the fleet manager's address %q must use https: over plain http anyone on the network can pose as the manager (set ENVORYX_FLEET_INSECURE=1 to allow it anyway)", raw)
	}
	return true, nil
}

// isLoopback reports whether host names this machine.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// Nudge has a connected agent report the instance's state now, after a change the
// manager should see at once (the support access).
func (a *Agent) Nudge() {
	if a == nil {
		return
	}
	select {
	case a.nudge <- struct{}{}:
	default:
	}
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
var errRevoked = errors.New("the fleet manager removed this instance; it keeps its last plan. To join again, set a new enrollment token in ENVORYX_FLEET_TOKEN and restart Envoryx")

// Run enrolls the instance when needed and keeps it connected until ctx ends or the
// manager removes it.
func (a *Agent) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		attempt := time.Now()
		err := a.runOnce(ctx)
		if errors.Is(err, errRevoked) {
			a.setErr(err)
			return
		}
		if ctx.Err() != nil {
			return
		}
		a.setErr(err)
		a.mu.Lock()
		since := a.connectedAt
		a.mu.Unlock()
		if since.After(attempt) {
			backoff = reconnectDelay(backoff, time.Since(since))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

// reconnectDelay is how long Run waits after a connection that held for held broke. One
// that held for a while is tried again soon. One the manager ended right away waits at
// least minReconnect, and longer each time, so a manager that accepts and drops at once
// doesn't get a connection a second.
func reconnectDelay(backoff, held time.Duration) time.Duration {
	if held >= stableConnection {
		return time.Second
	}
	return max(backoff, minReconnect)
}

func (a *Agent) runOnce(ctx context.Context) error {
	st, err := a.ensureEnrolled(ctx)
	if err != nil {
		return err
	}
	return a.connect(ctx, st)
}

// ensureEnrolled returns the instance's state, enrolling with the token first when the
// instance has none, ENVORYX_FLEET_URL points at another manager or ENVORYX_FLEET_TOKEN
// holds a token the instance hasn't tried yet.
func (a *Agent) ensureEnrolled(ctx context.Context) (*state, error) {
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	same := st != nil && (a.cfg.URL == "" || sameManager(st.URL, a.cfg.URL))
	newToken := a.cfg.URL != "" && a.cfg.Token != "" && (st == nil || st.TokenHash != tokenHash(a.cfg.Token))
	if same && !newToken {
		return st, nil
	}
	if same {
		// An enrolled instance with a new token: it joins again (removed by the manager,
		// or with a new key). A token the manager refuses, such as the one the instance
		// enrolled with that stayed set, is noted and the instance stays as it is.
		next, err := a.enroll(ctx)
		var refused *enrollRefused
		switch {
		case errors.As(err, &refused):
			a.cfg.Log.Info("fleet: ENVORYX_FLEET_TOKEN not used, the instance stays enrolled", "id", st.ID, "reason", refused.msg)
			kept := *st
			kept.TokenHash = tokenHash(a.cfg.Token)
			if err := a.saveState(&kept); err != nil {
				return nil, err
			}
			return &kept, nil
		case err != nil:
			// The manager is out of reach: the next attempt tries the token again.
			return nil, err
		}
		return next, nil
	}
	if a.cfg.Token == "" {
		if st != nil {
			// Another manager is configured but no token to join it: stay with the old one.
			return st, nil
		}
		return nil, errors.New("set ENVORYX_FLEET_TOKEN to the enrollment token from the fleet manager")
	}
	return a.enroll(ctx)
}

// enrollRefused is the manager's no to an enrollment token.
type enrollRefused struct {
	status int
	msg    string
}

func (e *enrollRefused) Error() string {
	return fmt.Sprintf("the fleet manager refused the enrollment (HTTP %d): %s", e.status, e.msg)
}

// enroll trades ENVORYX_FLEET_TOKEN for the manager's trust in a new key and saves the
// state.
func (a *Agent) enroll(ctx context.Context) (*state, error) {
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
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return nil, &enrollRefused{status: res.StatusCode, msg: apiMessage(raw)}
		}
		return nil, fmt.Errorf("the fleet manager refused the enrollment (HTTP %d): %s", res.StatusCode, apiMessage(raw))
	}
	var out enrollResponse
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return nil, errors.New("the fleet manager's answer to the enrollment is unreadable")
	}
	st := &state{URL: strings.TrimRight(a.cfg.URL, "/"), ID: out.ID, Name: out.Name, Key: priv.Seed(), TokenHash: tokenHash(a.cfg.Token)}
	if err := a.saveState(st); err != nil {
		return nil, err
	}
	a.cfg.Log.Info("fleet: enrolled", "url", st.URL, "id", st.ID, "name", st.Name)
	return st, nil
}

// saveState writes the state file and makes st the agent's state.
func (a *Agent) saveState(st *state) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := writeFileAtomic(filepath.Join(a.cfg.ConfigDir, StateFile), b); err != nil {
		return fmt.Errorf("saving the fleet enrollment: %w", err)
	}
	a.mu.Lock()
	a.st = st
	a.mu.Unlock()
	return nil
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
	a.connectedAt = a.contact
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
		if err := wsjson.Write(wctx, conn, message{Type: "status", Status: &s}); err != nil {
			return err
		}
		// A report that went through is contact too: the manager rarely writes on its own.
		a.mu.Lock()
		a.contact = time.Now()
		a.mu.Unlock()
		return nil
	}
	if err := sendStatus(); err != nil {
		return closeErr(err, "")
	}
	readErr := make(chan error, 1)
	planned := make(chan struct{}, 1)
	go func() {
		// One timer for the report after a suspension, however many the manager sends.
		var suspended *time.Timer
		defer func() {
			if suspended != nil {
				suspended.Stop()
			}
		}()
		for {
			var m message
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				readErr <- err
				return
			}
			a.mu.Lock()
			a.contact = time.Now()
			a.mu.Unlock()
			switch m.Type {
			case "plan":
				a.applyPlan(m.Plan)
			case "supportLogin":
				a.answer(ctx, conn, m, a.answerSupportLogin)
				continue
			case "backup":
				a.answer(ctx, conn, m, a.answerBackup)
				continue
			case "update":
				a.answer(ctx, conn, m, a.answerUpdate)
				continue
			case "certificate":
				a.applyCertificate(m.Certificate)
			case "suspension":
				a.applySuspension(m.Suspension)
				// Stopping the projects takes a moment; the manager hears the result too.
				if suspended == nil {
					suspended = time.AfterFunc(suspensionReport, func() {
						select {
						case planned <- struct{}{}:
						default:
						}
					})
				} else {
					suspended.Reset(suspensionReport)
				}
			default:
				continue
			}
			select {
			case planned <- struct{}{}:
			default:
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
			// The manager sees right away whether the plan or suspension was taken over.
			if err := sendStatus(); err != nil {
				return closeErr(err, "")
			}
		case <-a.nudge:
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
	if susp := a.cfg.Plan.Suspension(); susp != nil {
		s.SuspendedSince = susp.Since
	}
	a.mu.Lock()
	s.PlanError = a.planErr
	certErr := a.certErr
	a.mu.Unlock()
	if certErr != "" {
		// The previous certificate, if any, stays in use.
		if s.Certificate == nil {
			s.Certificate = &CertificateStatus{}
		}
		s.Certificate.Error = certErr
	}
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

// applySuspension writes the suspension the manager sent (null lifts it) and has it read.
func (a *Agent) applySuspension(raw json.RawMessage) {
	path := filepath.Join(a.cfg.ConfigDir, plan.SuspensionFile)
	var err error
	if len(raw) == 0 || string(raw) == "null" {
		if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else if _, err = plan.ParseSuspension(raw); err == nil {
		err = writeFileAtomic(path, raw)
	}
	if err != nil {
		a.cfg.Log.Warn("fleet: suspension not taken over", "err", err)
		return
	}
	a.cfg.Plan.Reload()
}

// applyCertificate writes the hoster's certificate (null takes it back) and has it read.
// The file holds the key, so only the owner may read it.
func (a *Agent) applyCertificate(raw json.RawMessage) {
	path := filepath.Join(a.cfg.ConfigDir, plan.CertificateFile)
	var err error
	if len(raw) == 0 || string(raw) == "null" {
		if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else if _, err = plan.ParseCertificate(raw); err == nil {
		err = writeFileAtomic(path, raw)
	}
	a.mu.Lock()
	a.certErr = ""
	if err != nil {
		a.certErr = err.Error()
	}
	a.mu.Unlock()
	if err != nil {
		a.cfg.Log.Warn("fleet: certificate not taken over", "err", err)
		return
	}
	a.cfg.Plan.Reload()
}

// answer runs handle for the manager's request in the background, unless a request of its
// type is being answered still: that one gets "busy" at once. Writes on the connection may
// run concurrently.
func (a *Agent) answer(ctx context.Context, conn *websocket.Conn, req message, handle func(context.Context, *websocket.Conn, message)) {
	a.mu.Lock()
	busy := a.busy[req.Type]
	a.busy[req.Type] = true
	a.mu.Unlock()
	if busy {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = wsjson.Write(wctx, conn, message{Type: req.Type, ID: req.ID, Error: "the instance is still answering the last request of this kind; try again in a moment"})
		return
	}
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.busy, req.Type)
			a.mu.Unlock()
		}()
		handle(ctx, conn, req)
	}()
}

// clipName shortens a name the manager sent (the hoster's, a version) to maxHosterName
// characters for the logs.
func clipName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxHosterName {
		return string(r[:maxHosterName])
	}
	return s
}

// answerSupportLogin answers the manager's request for a support login link.
func (a *Agent) answerSupportLogin(ctx context.Context, conn *websocket.Conn, req message) {
	reply := message{Type: "supportLogin", ID: req.ID}
	hoster := clipName(req.Name)
	if a.cfg.SupportLogin == nil {
		reply.Error = "this instance offers no support login"
	} else if url, err := a.cfg.SupportLogin(ctx, hoster); err != nil {
		reply.Error = err.Error()
	} else {
		reply.URL = url
	}
	a.cfg.Log.Info("fleet: support login requested", "hoster", hoster, "granted", reply.Error == "")
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = wsjson.Write(wctx, conn, reply)
}

// answerBackup starts the instance backup the manager asked for and says whether it
// started; the status tells how it went.
func (a *Agent) answerBackup(ctx context.Context, conn *websocket.Conn, req message) {
	reply := message{Type: "backup", ID: req.ID}
	if a.cfg.Backup == nil {
		reply.Error = "this instance takes no backups on request"
	} else if err := a.cfg.Backup(ctx); err != nil {
		reply.Error = err.Error()
	}
	a.cfg.Log.Info("fleet: instance backup requested", "started", reply.Error == "")
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = wsjson.Write(wctx, conn, reply)
	a.Nudge()
}

// answerUpdate starts the update the manager asked for and says whether it started.
func (a *Agent) answerUpdate(ctx context.Context, conn *websocket.Conn, req message) {
	reply := message{Type: "update", ID: req.ID}
	if a.cfg.Update == nil {
		reply.Error = "this instance doesn't update itself"
	} else if err := a.cfg.Update(ctx, req.Version); err != nil {
		reply.Error = err.Error()
	}
	a.cfg.Log.Info("fleet: update requested", "version", clipName(req.Version), "started", reply.Error == "")
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = wsjson.Write(wctx, conn, reply)
	a.Nudge()
}
