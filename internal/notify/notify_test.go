package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type received struct {
	path, contentType string
	headers           http.Header
	body              string
}

type sink struct {
	mu  sync.Mutex
	got []received
}

func (k *sink) count() int        { k.mu.Lock(); defer k.mu.Unlock(); return len(k.got) }
func (k *sink) at(i int) received { k.mu.Lock(); defer k.mu.Unlock(); return k.got[i] }
func (k *sink) last() received    { k.mu.Lock(); defer k.mu.Unlock(); return k.got[len(k.got)-1] }
func (k *sink) add(r received)    { k.mu.Lock(); defer k.mu.Unlock(); k.got = append(k.got, r) }

func newService(t *testing.T) (*Service, *sink, *httptest.Server) {
	t.Helper()
	got := &sink{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.add(received{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), headers: r.Header.Clone(), body: string(b)})
		if strings.HasSuffix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("nope"))
		}
	}))
	t.Cleanup(srv.Close)
	s, err := New(t.TempDir(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.telegramBase = srv.URL
	return s, got, srv
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestProvidersAndDeduplication(t *testing.T) {
	s, got, srv := newService(t)
	count := got.count

	// Disabled: nothing is sent.
	s.Notify(context.Background(), Event{Kind: "acme.failed", Level: Error, Title: "x", Message: "y"})
	time.Sleep(30 * time.Millisecond)
	if count() != 0 {
		t.Fatal("disabled service must not send")
	}

	if err := s.SetConfig(Config{Enabled: true, Provider: "ntfy", URL: "not a url"}); err == nil {
		t.Fatal("bad URL must be rejected")
	}
	if err := s.SetConfig(Config{Enabled: true, Provider: "ntfy", URL: srv.URL + "/envoryx", Token: "tk-1", Kinds: []string{"acme.failed", "project.unhealthy"}}); err != nil {
		t.Fatal(err)
	}
	s.Notify(context.Background(), Event{Kind: "acme.failed", Level: Error, Title: "Renewal failed", Message: "cloudflare: token rejected"})
	waitFor(t, func() bool { return count() == 1 })
	r := got.at(0)
	if r.path != "/envoryx" || r.headers.Get("Title") != "Renewal failed" || r.headers.Get("Priority") != "5" || r.headers.Get("Authorization") != "Bearer tk-1" || r.body != "cloudflare: token rejected" {
		t.Fatalf("ntfy request: %+v", r)
	}
	// Same key within the cooldown is suppressed; an unselected kind is ignored.
	s.Notify(context.Background(), Event{Kind: "acme.failed", Level: Error, Title: "Renewal failed", Message: "again"})
	s.Notify(context.Background(), Event{Kind: "acme.renewed", Level: Info, Title: "ok", Message: "ok"})
	time.Sleep(50 * time.Millisecond)
	if count() != 1 {
		t.Fatalf("cooldown/kind filter failed: %d deliveries", count())
	}
	// After the cooldown the event goes out again.
	s.mu.Lock()
	s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	s.mu.Unlock()
	s.Notify(context.Background(), Event{Kind: "acme.failed", Level: Error, Title: "Renewal failed", Message: "again"})
	waitFor(t, func() bool { return count() == 2 })
	// Different projects are separate keys.
	s.Notify(context.Background(), Event{Kind: "project.unhealthy", Level: Warning, Title: "Shop", Message: "stopped", Project: "shop"})
	s.Notify(context.Background(), Event{Kind: "project.unhealthy", Level: Warning, Title: "Blog", Message: "stopped", Project: "blog"})
	waitFor(t, func() bool { return count() == 4 })

	// Token is kept when re-saving without one and never returned.
	if err := s.SetConfig(Config{Enabled: true, Provider: "ntfy", URL: srv.URL + "/envoryx"}); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.HasToken || st.Config.Token != "" {
		t.Fatalf("status: %+v", st)
	}

	// Webhook, Discord, Slack, Telegram payloads.
	for _, tc := range []struct{ provider, want string }{
		{"webhook", `"kind":"test"`}, {"discord", `"content":"**Envoryx test notification**`}, {"slack", `"text":"*Envoryx test notification*`},
	} {
		if err := s.Test(context.Background(), Config{Provider: tc.provider, URL: srv.URL + "/" + tc.provider}); err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		last := got.last()
		if last.path != "/"+tc.provider || last.contentType != "application/json" || !strings.Contains(last.body, tc.want) {
			t.Fatalf("%s payload: %+v", tc.provider, last)
		}
	}
	if err := s.Test(context.Background(), Config{Provider: "telegram", Token: "123:abc", ChatID: "42"}); err != nil {
		t.Fatal(err)
	}
	last := got.last()
	var tg map[string]any
	_ = json.Unmarshal([]byte(last.body), &tg)
	if last.path != "/bot123:abc/sendMessage" || tg["chat_id"] != "42" {
		t.Fatalf("telegram: %+v", last)
	}
	// Failures are reported.
	if err := s.Test(context.Background(), Config{Provider: "webhook", URL: srv.URL + "/fail"}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("http failure must surface: %v", err)
	}
}

// fakeSMTP speaks just enough SMTP for net/smtp without TLS.
func fakeSMTP(t *testing.T) (net.Listener, *string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mail string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		rd := bufio.NewReader(conn)
		w := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					mail = data.String()
					w("250 ok")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				w("250-fake")
				w("250 AUTH PLAIN")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				w("235 ok")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				w("250 ok")
			case line == "DATA":
				w("354 go")
				inData = true
			case line == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln, &mail
}

func TestEmailDelivery(t *testing.T) {
	ln, mail := fakeSMTP(t)
	defer ln.Close()
	s, err := New(t.TempDir(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.smtpDial = func(ctx context.Context, addr string) (net.Conn, error) { return net.Dial("tcp", ln.Addr().String()) }
	cfg := Config{Provider: "email", SMTPHost: "localhost", SMTPPort: 25, SMTPSecurity: "none", SMTPUser: "u", SMTPPassword: "p", From: "envoryx@example.com", To: "me@example.com"}
	if err := s.Test(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*mail, "Subject: [Envoryx] Envoryx test notification") || !strings.Contains(*mail, "Notifications are working.") {
		t.Fatalf("mail: %q", *mail)
	}
	if err := s.SetConfig(Config{Enabled: true, Provider: "email", From: "a@b"}); err == nil {
		t.Fatal("incomplete smtp config must be rejected")
	}
}
