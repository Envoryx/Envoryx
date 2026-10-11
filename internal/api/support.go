package api

import (
	"fmt"
	"net/http"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
)

// supportAccess returns the admins' consent to the hoster's support and whether a
// hoster's fleet manages the instance at all.
func (a *API) supportAccess(w http.ResponseWriter, r *http.Request) {
	acc, err := a.d.Auth.SupportAccess(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access": acc, "managed": a.d.Fleet != nil})
}

// setSupportAccess stores the consent: {"allowed": false} takes it back and ends the
// support's sessions, {"allowed": true} allows until revoked, with "until" until then.
func (a *API) setSupportAccess(w http.ResponseWriter, r *http.Request) {
	var req auth.SupportAccess
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.SetSupportAccess(r.Context(), req); err != nil {
		writeError(w, r, err)
		return
	}
	details := map[string]any{"allowed": req.Allowed}
	if req.Allowed && !req.Until.IsZero() {
		details["until"] = req.Until
	}
	a.d.Audit.Log(r.Context(), audit.ActionSupportAccess, "settings", "", details)
	// The fleet manager offers the support login only while it is allowed.
	a.d.Fleet.Nudge()
	a.supportAccess(w, r)
}

// supportLogin opens a session for the hoster's support from the one-time link the fleet
// manager got (GET /api/v1/auth/support/{token}) and sends the browser to the dashboard.
func (a *API) supportLogin(w http.ResponseWriter, r *http.Request) {
	token, user, hoster, err := a.d.Auth.RedeemSupportLogin(r.Context(), r.PathValue("token"), auth.ClientIP(r), r.UserAgent())
	if err != nil {
		ssoFailed(w, r, err.Error())
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role, Support: true})
	a.d.Audit.Log(ctx, audit.ActionSupportLogin, "user", user.ID, map[string]any{"hoster": hoster})
	http.Redirect(w, r, "/", http.StatusFound)
}

// supportRefused are the routes the hoster's support can't use: everything that would
// give someone a way in that outlasts the admins' consent, or change that consent. The
// support manages no accounts: it can't invite, change, remove or re-invite users (a new
// invite link sets the password of an existing account), change their project roles or
// the single sign-on, create tokens or store SSH keys, set a password or restore an
// instance backup (which brings back that backup's accounts and tokens). The instance's SSH keys in PATCH /settings are refused in updateSettings.
var supportRefused = map[string]bool{
	"POST /api/v1/tokens":                        true,
	"PUT /api/v1/auth/ssh-keys":                  true,
	"POST /api/v1/auth/password":                 true,
	"PUT /api/v1/settings/support-access":        true,
	"POST /api/v1/users":                         true,
	"PATCH /api/v1/users/{id}":                   true,
	"DELETE /api/v1/users/{id}":                  true,
	"POST /api/v1/users/{id}/invite":             true,
	"PUT /api/v1/users/{id}/projects/{project}":  true,
	"PUT /api/v1/settings/oidc":                  true,
	"POST /api/v1/settings/oidc/test":            true,
	"POST /api/v1/instance/backups/{id}/restore": true,
}

// errSupportRefused answers what the hoster's support can't do.
var errSupportRefused = fmt.Errorf("%w: the hoster's support can't do this", auth.ErrForbidden)
