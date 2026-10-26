package api

import (
	"fmt"
	"net/http"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
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
	// The consent lets the hoster in as an admin: an API token can't give it.
	if !sessionOnly(w, r, errAccessNeedsSession) {
		return
	}
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
		a.ssoFailed(w, r, err.Error())
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(r, token))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role, Support: true})
	a.d.Audit.Log(ctx, audit.ActionSupportLogin, "user", user.ID, map[string]any{"hoster": hoster})
	http.Redirect(w, r, "/", http.StatusFound)
}

// supportRefused are the routes the hoster's support can't use: everything that would
// give someone a way in that outlasts the admins' consent, or change that consent. The
// support manages no accounts: it can't invite, change, remove or re-invite users (a new
// invite link sets the password of an existing account), change their project roles or
// the single sign-on, create tokens or store SSH keys, set a password or restore an
// instance backup (which brings back that backup's accounts and tokens). Nor can it show
// or replace the secret key: the hoster gets the instance backups through its offsite
// target already, and with the key it could read every secret in them. It brings no
// code of its own choosing to stay on the instance either: no addons, no custom images
// and no registry credentials for them. It doesn't decide where the backups go either: an
// offsite target it set up would keep receiving them after the consent ends, so it can't
// add, change or remove one. And it leaves the record of its visit alone: it can't
// shorten the audit log's retention or clear the log history.
//
// Refused in the handlers by what the body carries: the instance's SSH keys and the log
// history in PATCH /settings (updateSettings), and a custom image in a manifest
// (supportManifestImages); by what the backup holds: a project with a custom image in
// POST /backups/{project}/{backup}/restore-new (restoreBackupNew). What the support can still set up that outlasts the consent is
// what any admin works with in a project: workers, cron jobs and the project's files; the
// audit log records all of it under the support user.
var supportRefused = map[string]bool{
	"POST /api/v1/tokens":                             true,
	"PUT /api/v1/auth/ssh-keys":                       true,
	"POST /api/v1/auth/password":                      true,
	"PUT /api/v1/settings/support-access":             true,
	"POST /api/v1/users":                              true,
	"PATCH /api/v1/users/{id}":                        true,
	"DELETE /api/v1/users/{id}":                       true,
	"POST /api/v1/users/{id}/invite":                  true,
	"PUT /api/v1/users/{id}/projects/{project}":       true,
	"PUT /api/v1/settings/oidc":                       true,
	"POST /api/v1/settings/oidc/test":                 true,
	"POST /api/v1/instance/backups/{id}/restore":      true,
	"POST /api/v1/settings/secret-key/reveal":         true,
	"POST /api/v1/settings/secret-key/rotate":         true,
	"POST /api/v1/settings/deploy-key/regenerate":     true,
	"POST /api/v1/addons":                             true,
	"PUT /api/v1/projects/{id}/services/{kind}/image": true,
	"PUT /api/v1/settings/registries":                 true,
	"PUT /api/v1/audit/settings":                      true,
	"DELETE /api/v1/settings/log-history":             true,
	"POST /api/v1/offsite/targets":                    true,
	"PUT /api/v1/offsite/targets/{target}":            true,
	"DELETE /api/v1/offsite/targets/{target}":         true,
	// Second factors: the support has no password to protect, and resetting a user's
	// would open their account to whoever knows the password.
	"POST /api/v1/auth/2fa/totp/setup":           true,
	"POST /api/v1/auth/2fa/totp":                 true,
	"DELETE /api/v1/auth/2fa/totp":               true,
	"POST /api/v1/auth/2fa/passkeys/options":     true,
	"POST /api/v1/auth/2fa/passkeys":             true,
	"PATCH /api/v1/auth/2fa/passkeys/{passkey}":  true,
	"DELETE /api/v1/auth/2fa/passkeys/{passkey}": true,
	"POST /api/v1/auth/2fa/recovery-codes":       true,
	"DELETE /api/v1/users/{id}/two-factor":       true,
}

// errSupportRefused answers what the hoster's support can't do.
var errSupportRefused = fmt.Errorf("%w: the hoster's support can't do this", auth.ErrForbidden)

// supportManifestImages refuses the hoster's support a manifest that gives a runtime a
// custom image other than the one it has (current is nil for a new project), as
// PUT /projects/{id}/services/{kind}/image is refused to it.
func supportManifestImages(r *http.Request, mf manifest.Manifest, current *store.Project) error {
	if p, _ := auth.PrincipalFrom(r.Context()); !p.Support {
		return nil
	}
	for name, c := range mf.CustomImages() {
		if c.Image == "" && c.Dockerfile == "" {
			continue
		}
		if current != nil {
			if svc := current.Service(store.ServiceKind(name)); svc != nil && svc.Custom.Image == c.Image && svc.Custom.Dockerfile == c.Dockerfile {
				continue
			}
		}
		return errSupportRefused
	}
	return nil
}
