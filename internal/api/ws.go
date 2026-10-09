package api

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// wsRequest refuses a request to a WebSocket route that isn't a WebSocket upgrade from
// an allowed origin, before the handler starts anything. The handlers open their session
// (an action, a test run, a shell) before upgrading so failures arrive as HTTP errors,
// and websocket.Accept checks the origin only then: without this a page elsewhere could
// navigate the browser to the route (a top-level GET carries the SameSite=Lax cookie)
// and start an action, or open the socket cross-site, with the user's session. It uses
// the origin rules websocket.Accept applies afterwards.
func (a *API) wsRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, r, newError(http.StatusBadRequest, "bad_request", "this route only accepts WebSocket connections"))
		return false
	}
	if !originAllowed(r, a.d.AllowedOriginHosts) {
		writeError(w, r, newError(http.StatusForbidden, "forbidden", "origin not allowed"))
		return false
	}
	return true
}

func originAllowed(r *http.Request, extra []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(r.Host, u.Host) {
		return true
	}
	for _, pattern := range extra {
		target := u.Host
		if strings.Contains(pattern, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}
