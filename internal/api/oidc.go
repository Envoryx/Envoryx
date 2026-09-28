package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/oidc"
)

// oidcStateCookie binds a sign-in to the browser that started it.
const oidcStateCookie = "envoryx_oidc"

// oidcStatus tells the login page whether to offer single sign-on: GET /auth/oidc.
// Public, and nothing in it is secret.
func (a *API) oidcStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": false}
	if a.d.OIDC != nil {
		if c, err := a.d.OIDC.Config(r.Context()); err == nil && c.Enabled {
			out = map[string]any{"enabled": true, "name": c.Name}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// baseURL is Envoryx's address as the browser uses it.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// safeReturn keeps the page to continue on inside the UI: a local path, never another
// origin.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.HasPrefix(p, "/api/") {
		return "/"
	}
	return p
}

// ssoFailed returns to the login page with the reason.
func ssoFailed(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(reason), http.StatusFound)
}

// oidcStart sends the browser to the provider: GET /auth/oidc/start?return=/path.
func (a *API) oidcStart(w http.ResponseWriter, r *http.Request) {
	if a.d.OIDC == nil {
		ssoFailed(w, r, oidc.ErrDisabled.Error())
		return
	}
	redirect := baseURL(r) + "/api/v1/auth/oidc/callback"
	authURL, state, err := a.d.OIDC.Start(r.Context(), redirect, safeReturn(r.URL.Query().Get("return")))
	if err != nil {
		a.d.Log.Warn("single sign-on could not start", "err", err)
		ssoFailed(w, r, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: state, Path: "/api/v1/auth/oidc", MaxAge: 600, HttpOnly: true, Secure: a.d.Config.SecureCookies, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// oidcCallback finishes the sign-in the provider returns from: GET /auth/oidc/callback.
func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: a.d.Config.SecureCookies, SameSite: http.SameSiteLaxMode})
	if a.d.OIDC == nil {
		ssoFailed(w, r, oidc.ErrDisabled.Error())
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		ssoFailed(w, r, "the provider refused: "+e+" "+q.Get("error_description"))
		return
	}
	state := q.Get("state")
	c, err := r.Cookie(oidcStateCookie)
	if err != nil || state == "" || c.Value != state {
		ssoFailed(w, r, "the sign-in did not start in this browser; start it again")
		return
	}
	user, returnTo, err := a.d.OIDC.Finish(r.Context(), state, q.Get("code"))
	if err != nil {
		a.d.Audit.LogAs(r.Context(), "", audit.ActionLoginFailed, "user", "", map[string]string{"reason": err.Error(), "via": "sso"})
		if errors.Is(err, auth.ErrInvalidCredentials) {
			err = errors.New("this account is disabled")
		}
		ssoFailed(w, r, err.Error())
		return
	}
	token, err := a.d.Auth.StartSession(r.Context(), user, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		ssoFailed(w, r, err.Error())
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	a.d.Audit.LogAs(r.Context(), user.Username, audit.ActionLogin, "user", user.ID, map[string]string{"via": "sso"})
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// oidcSettingsDTO is the configuration without the client secret.
type oidcSettingsDTO struct {
	oidc.Config
	HasSecret bool `json:"hasSecret"`
	// RedirectURL is what to register at the provider.
	RedirectURL string `json:"redirectUrl"`
}

func (a *API) oidcSettingsOut(r *http.Request, c oidc.Config) oidcSettingsDTO {
	d := oidcSettingsDTO{Config: c, HasSecret: c.ClientSecret != "", RedirectURL: baseURL(r) + "/api/v1/auth/oidc/callback"}
	d.ClientSecret = ""
	return d
}

// oidcSettings returns the configuration: GET /settings/oidc.
func (a *API) oidcSettings(w http.ResponseWriter, r *http.Request) {
	if a.d.OIDC == nil {
		writeError(w, r, oidc.ErrDisabled)
		return
	}
	c, err := a.d.OIDC.Config(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"oidc": a.oidcSettingsOut(r, c)})
}

// setOIDCSettings stores it: PUT /settings/oidc. An empty client secret keeps the old one.
func (a *API) setOIDCSettings(w http.ResponseWriter, r *http.Request) {
	if a.d.OIDC == nil {
		writeError(w, r, oidc.ErrDisabled)
		return
	}
	var req oidc.Config
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := a.d.OIDC.SetConfig(r.Context(), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionSettingsChanged, "settings", "oidc", map[string]any{"enabled": c.Enabled, "issuer": c.Issuer, "autoCreate": c.AutoCreate, "defaultRole": c.DefaultRole})
	writeJSON(w, http.StatusOK, map[string]any{"oidc": a.oidcSettingsOut(r, c)})
}

// testOIDCSettings checks that the issuer answers: POST /settings/oidc/test.
func (a *API) testOIDCSettings(w http.ResponseWriter, r *http.Request) {
	if a.d.OIDC == nil {
		writeError(w, r, oidc.ErrDisabled)
		return
	}
	var req oidc.Config
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.Enabled = true
	if err := a.d.OIDC.Test(r.Context(), req); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
