package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"rsc.io/qr"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

// Two-factor authentication (auth/twofactor.go, auth/passkey.go): the second step of a
// password sign-in, the sign-in with a passkey alone, the own factors on the account
// page, and an admin resetting another user's.

// errTwoFactorSetup answers a session that has to set up a second factor first.
var errTwoFactorSetup = newError(http.StatusForbidden, "two_factor_setup", "this instance requires two-factor authentication: set up a second factor first")

// errFactorsNeedSession refuses API tokens the second factors: they are the account's
// own, managed in a browser session.
var errFactorsNeedSession = newError(http.StatusForbidden, "forbidden", "API tokens cannot manage two-factor authentication; sign in with a browser session")

// routesWhileTwoFactorSetup are what a session that has to set up a second factor first
// may still call: who is signed in, the plan (a suspension), and the setup itself.
var routesWhileTwoFactorSetup = map[string]bool{
	"GET /api/v1/auth/me":                    true,
	"GET /api/v1/plan":                       true,
	"GET /api/v1/auth/2fa":                   true,
	"POST /api/v1/auth/2fa/totp/setup":       true,
	"POST /api/v1/auth/2fa/totp":             true,
	"POST /api/v1/auth/2fa/passkeys/options": true,
	"POST /api/v1/auth/2fa/passkeys":         true,
}

// signedIn sets the session cookie and answers with the user, as the sign-in does.
func (a *API) signedIn(w http.ResponseWriter, r *http.Request, token string, user userDTO, method string) {
	http.SetCookie(w, a.d.Auth.Cookie(r, token))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role})
	a.d.Audit.Log(ctx, audit.ActionLogin, "user", user.ID, map[string]string{"method": method})
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// secondFactor finishes a password sign-in with a TOTP or recovery code: POST
// /auth/login/code with {"ticket", "code"} or {"ticket", "recoveryCode"}. Public; the
// ticket from the password step is the credential.
func (a *API) secondFactor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket       string `json:"ticket"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Code) > 16 || len(req.RecoveryCode) > 32 {
		writeError(w, r, auth.ErrInvalidCode)
		return
	}
	var (
		token  string
		user   store.User
		err    error
		method = auth.MethodTOTP
	)
	name := a.d.Auth.TicketUsername(r.Context(), req.Ticket)
	if req.RecoveryCode != "" {
		method = auth.MethodRecovery
		token, user, err = a.d.Auth.CompleteRecovery(r.Context(), req.Ticket, req.RecoveryCode, auth.ClientIP(r), r.UserAgent())
	} else {
		token, user, err = a.d.Auth.CompleteTOTP(r.Context(), req.Ticket, req.Code, auth.ClientIP(r), r.UserAgent())
	}
	if err != nil {
		if name != "" {
			a.d.Audit.LogAs(r.Context(), name, audit.ActionLoginFailed, "user", "", map[string]string{"reason": err.Error(), "method": method})
		}
		writeError(w, r, err)
		return
	}
	if method == auth.MethodRecovery {
		ctx := auth.WithPrincipal(r.Context(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role})
		a.d.Audit.Log(ctx, audit.ActionRecoveryCodeUsed, "user", user.ID, nil)
	}
	a.signedIn(w, r, token, userDTO{ID: user.ID, Username: user.Username, Role: user.Role}, method)
}

// relyingParty is where the browser runs a passkey ceremony: the request's Origin, which
// the CSRF check has held to the request's host.
func relyingParty(r *http.Request) (auth.RelyingParty, error) {
	return auth.RelyingPartyFor(r.Header.Get("Origin"))
}

// passkeyOptions starts a sign-in with a passkey: POST /auth/passkey/options with an
// optional {"ticket"} from the password step. Public.
func (a *API) passkeyOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rp, err := relyingParty(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, assertion, err := a.d.Auth.BeginPasskeyLogin(r.Context(), rp, req.Ticket)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "publicKey": assertion.Response})
}

// passkeyLogin finishes a sign-in with a passkey: POST /auth/passkey with {"id",
// "credential"} (the browser's PublicKeyCredential as JSON). Public.
func (a *API) passkeyLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string          `json:"id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	token, user, err := a.d.Auth.FinishPasskeyLogin(r.Context(), req.ID, req.Credential, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		if user.Username != "" {
			a.d.Audit.LogAs(r.Context(), user.Username, audit.ActionLoginFailed, "user", "", map[string]string{"reason": err.Error(), "method": auth.MethodPasskey})
		}
		writeError(w, r, err)
		return
	}
	a.signedIn(w, r, token, userDTO{ID: user.ID, Username: user.Username, Role: user.Role}, auth.MethodPasskey)
}

// ownFactors resolves the session whose own factors a request manages.
func ownFactors(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" {
		writeError(w, r, errFactorsNeedSession)
		return auth.Principal{}, false
	}
	return p, true
}

// setupError words a rejected code or passkey while setting up a factor. The caller is
// signed in, so it is a 422 and not the 401 the sign-in answers with: the web UI treats
// any 401 as an ended session and would throw the user out of the setup.
func setupError(err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCode):
		return newError(http.StatusUnprocessableEntity, "invalid_code", err.Error())
	case errors.Is(err, auth.ErrPasskey):
		return newError(http.StatusUnprocessableEntity, "passkey_rejected", err.Error())
	}
	return err
}

// twoFactorStatus answers what the caller has set up: GET /auth/2fa.
func (a *API) twoFactorStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	st, err := a.d.Auth.TwoFactorStatus(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type passwordConfirm struct {
	Password string `json:"password"`
}

// beginTOTP makes a TOTP secret to scan: POST /auth/2fa/totp/setup with {"password"}.
func (a *API) beginTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req passwordConfirm
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	secret, uri, err := a.d.Auth.BeginTOTP(r.Context(), p, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "uri": uri, "qr": qrSVG(uri)})
}

// confirmTOTP switches TOTP on with a first code: POST /auth/2fa/totp with {"code"}.
func (a *API) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	codes, err := a.d.Auth.ConfirmTOTP(r.Context(), p, req.Code)
	if err != nil {
		writeError(w, r, setupError(err))
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionTOTPEnabled, "user", p.UserID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// removeTOTP switches TOTP off: DELETE /auth/2fa/totp with {"password"}.
func (a *API) removeTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req passwordConfirm
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.RemoveTOTP(r.Context(), p, req.Password); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionTOTPDisabled, "user", p.UserID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// passkeyRegistrationOptions starts adding a passkey: POST /auth/2fa/passkeys/options
// with {"password"}.
func (a *API) passkeyRegistrationOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req passwordConfirm
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rp, err := relyingParty(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, creation, err := a.d.Auth.BeginPasskeyRegistration(r.Context(), p, req.Password, rp)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "publicKey": creation.Response})
}

// addPasskey stores the passkey the browser created: POST /auth/2fa/passkeys with
// {"id", "name", "credential"}.
func (a *API) addPasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	key, codes, err := a.d.Auth.FinishPasskeyRegistration(r.Context(), p, req.ID, req.Name, req.Credential)
	if err != nil {
		writeError(w, r, setupError(err))
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionPasskeyAdded, "user", p.UserID, map[string]string{"passkey": key.Name, "host": key.RPID})
	writeJSON(w, http.StatusCreated, map[string]any{"passkey": key, "recoveryCodes": codes})
}

// renamePasskey renames one of the caller's passkeys: PATCH /auth/2fa/passkeys/{passkey}
// with {"name"}.
func (a *API) renamePasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.RenamePasskey(r.Context(), p, r.PathValue("passkey"), req.Name); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removePasskey removes one of the caller's passkeys: DELETE
// /auth/2fa/passkeys/{passkey} with {"password"}.
func (a *API) removePasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req passwordConfirm
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.RemovePasskey(r.Context(), p, r.PathValue("passkey"), req.Password); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionPasskeyRemoved, "user", p.UserID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// regenerateRecoveryCodes replaces the caller's recovery codes: POST
// /auth/2fa/recovery-codes with {"password"}.
func (a *API) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, ok := ownFactors(w, r)
	if !ok {
		return
	}
	var req passwordConfirm
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	codes, err := a.d.Auth.RegenerateRecoveryCodes(r.Context(), p, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionRecoveryCodesRenewed, "user", p.UserID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// resetTwoFactor removes another user's second factors: DELETE /users/{id}/two-factor.
func (a *API) resetTwoFactor(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" {
		writeError(w, r, errUsersNeedSession)
		return
	}
	u, err := a.d.Auth.ResetTwoFactor(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionTwoFactorReset, "user", u.ID, map[string]string{"username": u.Username})
	w.WriteHeader(http.StatusNoContent)
}

// qrSVG draws text as a QR code in SVG, for the authenticator app to scan.
func qrSVG(text string) string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 4
	size := code.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, size, size, size, size)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}
