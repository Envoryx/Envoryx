package api

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/proxy"
)

// The DNS check accepts a name only when this instance's proxy answers the probe host
// there: another Envoryx (another token) or no answer at all is not a confirmation.
func TestProbeProxyTellsItsOwnProxyApart(t *testing.T) {
	const host = "envoryx-diagnostics-probe.test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := func(context.Context) (proxy.Table, error) {
		return proxy.Table{ProbeHost: host, ProbeToken: "this-instance"}, nil
	}
	srv := httptest.NewServer(proxy.NewHandler(proxy.NewRouter(source, time.Second, log), http.NotFoundHandler(), false, log))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	ctx := context.Background()

	if got := probeProxy(ctx, []string{"127.0.0.1"}, port, host, "this-instance"); got != probeOwn {
		t.Fatalf("own proxy: %v", got)
	}
	if got := probeProxy(ctx, []string{"127.0.0.1"}, port, host, "another-instance"); got != probeOther {
		t.Fatalf("another instance's proxy: %v", got)
	}
	// Something that is not an Envoryx proxy at all.
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>router</html>")) }))
	defer web.Close()
	wu, _ := url.Parse(web.URL)
	webPort, _ := strconv.Atoi(wu.Port())
	if got := probeProxy(ctx, []string{"127.0.0.1"}, webPort, host, "this-instance"); got != probeOther {
		t.Fatalf("a web server: %v", got)
	}
	// Nothing listening, or no port to ask on.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if got := probeProxy(ctx, []string{"127.0.0.1"}, closed, host, "this-instance"); got != probeNone {
		t.Fatalf("nothing listening: %v", got)
	}
	if got := probeProxy(ctx, []string{"127.0.0.1"}, 0, host, "this-instance"); got != probeNone {
		t.Fatalf("no port: %v", got)
	}
}
