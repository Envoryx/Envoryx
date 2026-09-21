// Package notify delivers operational events (failed certificate renewals, unhealthy
// projects, failed project creation …) to a webhook, ntfy, Discord, Slack, Telegram or
// e-mail. Delivery is best effort and never blocks the caller.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is the severity of an event.
type Level string

const (
	Info    Level = "info"
	Warning Level = "warning"
	Error   Level = "error"
)

// Event is something worth telling the operator about.
type Event struct {
	// Kind is a stable key used for filtering and de-duplication (e.g. "acme.failed").
	Kind    string
	Level   Level
	Title   string
	Message string
	Project string
	// Key overrides the de-duplication key (default Kind+Project).
	Key string
}

// Sender is what event sources depend on.
type Sender interface {
	Notify(ctx context.Context, e Event)
	// Clear forgets the de-duplication state of a key so the next event is delivered
	// immediately (used when a condition recovers).
	Clear(key string)
}

// Kinds lists the supported event kinds with a description for the UI.
var Kinds = []struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}{
	{"project.unhealthy", "A project that should be running is stopped or broken (and when it recovers)", true},
	{"project.failed", "Creating a project failed and was rolled back", true},
	{"acme.failed", "Let's Encrypt certificate could not be issued or renewed", true},
	{"acme.renewed", "Let's Encrypt certificate issued or renewed", false},
	{"backup.failed", "A backup could not be created", true},
	{"envoryx.started", "Envoryx started", false},
	{"projects.resumed", "Envoryx started the projects again that were running before it was stopped", true},
	{"docker.orphans_removed", "Envoryx removed orphaned containers or networks that belonged to no project", true},
	{"envoryx.failed", "Envoryx could not start, or a background task crashed and was restarted", true},
	{"storage.low", "Free disk space under /config, /projects or /backups is running out (and when it recovers)", true},
}

// Providers lists the supported delivery channels.
var Providers = map[string]string{
	"webhook":  "Generic webhook (JSON POST)",
	"ntfy":     "ntfy",
	"discord":  "Discord webhook",
	"slack":    "Slack incoming webhook",
	"telegram": "Telegram bot",
	"email":    "E-mail (SMTP)",
}

// Config is the operator configuration. Token and Password are secrets.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	// URL is the webhook / ntfy topic / Discord / Slack URL.
	URL string `json:"url,omitempty"`
	// Token is an ntfy access token or the Telegram bot token.
	Token string `json:"token,omitempty"`
	// ChatID is the Telegram chat id.
	ChatID string `json:"chatId,omitempty"`
	// SMTP settings for the e-mail provider.
	SMTPHost     string `json:"smtpHost,omitempty"`
	SMTPPort     int    `json:"smtpPort,omitempty"`
	SMTPUser     string `json:"smtpUser,omitempty"`
	SMTPPassword string `json:"smtpPassword,omitempty"`
	// SMTPSecurity is "starttls" (default), "tls" or "none".
	SMTPSecurity string `json:"smtpSecurity,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	// Kinds enables event kinds; nil = defaults.
	Kinds []string `json:"kinds"`
}

// Public strips secrets for API responses.
func (c Config) Public() Config {
	c.Token = ""
	c.SMTPPassword = ""
	return c
}

// enabledKind reports whether an event kind is selected.
func (c Config) enabledKind(kind string) bool {
	if c.Kinds == nil {
		for _, k := range Kinds {
			if k.Kind == kind {
				return k.Default
			}
		}
		return false
	}
	for _, k := range c.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Status is reported to the UI.
type Status struct {
	Config    Config     `json:"config"`
	HasToken  bool       `json:"hasToken"`
	HasSMTPPW bool       `json:"hasSmtpPassword"`
	LastSent  *time.Time `json:"lastSent,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

const configFile = "notify.json"

// Service holds the configuration and delivers events.
type Service struct {
	dir  string
	log  *slog.Logger
	http *http.Client

	mu        sync.Mutex
	cfg       Config
	lastSent  *time.Time
	lastError string
	// recent maps de-duplication keys to the last delivery time.
	recent map[string]time.Time
	now    func() time.Time
	// Test hooks.
	smtpDial     func(ctx context.Context, addr string) (net.Conn, error)
	telegramBase string
}

// cooldown suppresses repeated deliveries of the same key.
var cooldown = map[string]time.Duration{
	"project.unhealthy": 6 * time.Hour,
	"acme.failed":       24 * time.Hour,
	"backup.failed":     time.Hour,
	"storage.low":       24 * time.Hour,
}

// New loads the configuration from dir.
func New(dir string, log *slog.Logger) (*Service, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create notify directory: %w", err)
	}
	s := &Service{dir: dir, log: log, http: &http.Client{Timeout: 20 * time.Second}, recent: map[string]time.Time{}, now: time.Now}
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if err == nil {
		if err := json.Unmarshal(raw, &s.cfg); err != nil {
			log.Warn("notification configuration unreadable; ignoring", "err", err)
			s.cfg = Config{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read notification configuration: %w", err)
	}
	return s, nil
}

// Status returns the state for the UI.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Config: s.cfg.Public(), HasToken: s.cfg.Token != "", HasSMTPPW: s.cfg.SMTPPassword != "", LastSent: s.lastSent, LastError: s.lastError}
}

// SetConfig validates and stores a configuration. Empty secrets keep the stored ones.
func (s *Service) SetConfig(cfg Config) error {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.URL, cfg.Token, cfg.ChatID = strings.TrimSpace(cfg.URL), strings.TrimSpace(cfg.Token), strings.TrimSpace(cfg.ChatID)
	cfg.SMTPHost, cfg.SMTPUser, cfg.From, cfg.To = strings.TrimSpace(cfg.SMTPHost), strings.TrimSpace(cfg.SMTPUser), strings.TrimSpace(cfg.From), strings.TrimSpace(cfg.To)
	cfg.SMTPSecurity = strings.ToLower(strings.TrimSpace(cfg.SMTPSecurity))
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.Token == "" {
		cfg.Token = s.cfg.Token
	}
	if cfg.SMTPPassword == "" {
		cfg.SMTPPassword = s.cfg.SMTPPassword
	}
	if cfg.Enabled {
		if err := validateConfig(cfg); err != nil {
			return err
		}
	}
	if cfg.Kinds != nil {
		known := map[string]bool{}
		for _, k := range Kinds {
			known[k.Kind] = true
		}
		for _, k := range cfg.Kinds {
			if !known[k] {
				return fmt.Errorf("unknown event kind %q", k)
			}
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.dir, configFile), raw, 0o600); err != nil {
		return fmt.Errorf("write notification configuration: %w", err)
	}
	s.cfg = cfg
	s.lastError = ""
	s.recent = map[string]time.Time{}
	return nil
}

func validateConfig(c Config) error {
	if _, ok := Providers[c.Provider]; !ok {
		return fmt.Errorf("unsupported notification provider %q", c.Provider)
	}
	httpsURL := func(u string) error {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
			return errors.New("a valid http(s) URL is required")
		}
		return nil
	}
	switch c.Provider {
	case "webhook", "ntfy", "discord", "slack":
		return httpsURL(c.URL)
	case "telegram":
		if c.Token == "" || c.ChatID == "" {
			return errors.New("telegram needs the bot token and chat id")
		}
	case "email":
		if c.SMTPHost == "" || c.From == "" || c.To == "" {
			return errors.New("e-mail needs SMTP host, sender and recipient")
		}
		switch c.SMTPSecurity {
		case "", "starttls", "tls", "none":
		default:
			return errors.New("smtp security must be starttls, tls or none")
		}
		if c.SMTPPort < 0 || c.SMTPPort > 65535 {
			return errors.New("invalid smtp port")
		}
	}
	return nil
}

// Notify delivers an event asynchronously when enabled and not de-duplicated.
func (s *Service) Notify(ctx context.Context, e Event) {
	cfg, at, ok := s.admit(e)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		s.send(ctx, cfg, e, at)
	}()
}

// NotifySync is Notify for callers that are about to exit (a failed start): it waits for
// the delivery and reports whether it succeeded.
func (s *Service) NotifySync(ctx context.Context, e Event) error {
	cfg, at, ok := s.admit(e)
	if !ok {
		return nil
	}
	return s.send(ctx, cfg, e, at)
}

// admit applies the enabled/kind/cooldown filters and returns the configuration snapshot
// to deliver with.
func (s *Service) admit(e Event) (Config, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	if !cfg.Enabled || !cfg.enabledKind(e.Kind) {
		return Config{}, time.Time{}, false
	}
	key := e.Key
	if key == "" {
		key = e.Kind + "|" + e.Project
	}
	if cd := cooldown[e.Kind]; cd > 0 {
		if last, ok := s.recent[key]; ok && s.now().Sub(last) < cd {
			return Config{}, time.Time{}, false
		}
		s.recent[key] = s.now()
	}
	return cfg, s.now(), true
}

func (s *Service) send(ctx context.Context, cfg Config, e Event, at time.Time) error {
	if err := s.deliver(ctx, cfg, e, at); err != nil {
		s.log.Warn("notification delivery failed", "kind", e.Kind, "err", err)
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	ts := s.now()
	s.lastSent, s.lastError = &ts, ""
	s.mu.Unlock()
	return nil
}

// Clear implements Sender.
func (s *Service) Clear(key string) {
	s.mu.Lock()
	delete(s.recent, key)
	s.mu.Unlock()
}

// Test sends a test event synchronously with the given (unsaved) configuration; empty
// secrets fall back to the stored ones.
func (s *Service) Test(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	if cfg.Token == "" {
		cfg.Token = s.cfg.Token
	}
	if cfg.SMTPPassword == "" {
		cfg.SMTPPassword = s.cfg.SMTPPassword
	}
	s.mu.Unlock()
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if err := validateConfig(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	at := s.now()
	s.mu.Unlock()
	return s.deliver(ctx, cfg, Event{Kind: "test", Level: Info, Title: "Envoryx test notification", Message: "Notifications are working."}, at)
}

func (s *Service) deliver(ctx context.Context, cfg Config, e Event, at time.Time) error {
	switch cfg.Provider {
	case "webhook":
		return s.postJSON(ctx, cfg.URL, nil, map[string]any{"kind": e.Kind, "level": e.Level, "title": e.Title, "message": e.Message, "project": e.Project, "time": at.UTC().Format(time.RFC3339)})
	case "ntfy":
		h := http.Header{"Title": {e.Title}, "Priority": {ntfyPriority(e.Level)}, "Tags": {ntfyTag(e.Level)}}
		if cfg.Token != "" {
			h.Set("Authorization", "Bearer "+cfg.Token)
		}
		return s.post(ctx, cfg.URL, h, "text/plain; charset=utf-8", []byte(e.Message))
	case "discord":
		return s.postJSON(ctx, cfg.URL, nil, map[string]any{"content": fmt.Sprintf("**%s**\n%s", e.Title, e.Message)})
	case "slack":
		return s.postJSON(ctx, cfg.URL, nil, map[string]any{"text": fmt.Sprintf("*%s*\n%s", e.Title, e.Message)})
	case "telegram":
		u := "https://api.telegram.org/bot" + url.PathEscape(cfg.Token) + "/sendMessage"
		if s.telegramBase != "" {
			u = s.telegramBase + "/bot" + url.PathEscape(cfg.Token) + "/sendMessage"
		}
		return s.postJSON(ctx, u, nil, map[string]any{"chat_id": cfg.ChatID, "text": e.Title + "\n" + e.Message})
	case "email":
		return s.sendMail(ctx, cfg, e, at)
	}
	return fmt.Errorf("unsupported provider %q", cfg.Provider)
}

func ntfyPriority(l Level) string {
	switch l {
	case Error:
		return "5"
	case Warning:
		return "4"
	}
	return "3"
}

func ntfyTag(l Level) string {
	switch l {
	case Error:
		return "rotating_light"
	case Warning:
		return "warning"
	}
	return "white_check_mark"
}

func (s *Service) postJSON(ctx context.Context, u string, h http.Header, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return s.post(ctx, u, h, "application/json", b)
}

func (s *Service) post(ctx context.Context, u string, h http.Header, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Envoryx")
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("%s: %s %s", req.URL.Host, res.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (s *Service) sendMail(ctx context.Context, cfg Config, e Event, at time.Time) error {
	port := cfg.SMTPPort
	security := cfg.SMTPSecurity
	if security == "" {
		security = "starttls"
	}
	if port == 0 {
		port = 587
		if security == "tls" {
			port = 465
		}
	}
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(port))
	var conn net.Conn
	var err error
	if s.smtpDial != nil {
		conn, err = s.smtpDial(ctx, addr)
	} else if security == "tls" {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}}
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer c.Close()
	if security == "starttls" && s.smtpDial == nil {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if cfg.SMTPUser != "" {
		// net/smtp refuses PLAIN auth over an unencrypted connection (except localhost).
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, to := range strings.Split(cfg.To, ",") {
		if to = strings.TrimSpace(to); to != "" {
			if err := c.Rcpt(to); err != nil {
				return err
			}
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	subject := "[Envoryx] " + e.Title
	body := e.Message
	if e.Project != "" {
		body = "Project: " + e.Project + "\n\n" + body
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		cfg.From, cfg.To, subject, at.Format(time.RFC1123Z), strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err := io.WriteString(w, msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
