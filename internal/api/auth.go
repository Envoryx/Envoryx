package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/validate"
)

type userDTO struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (a *API) setupStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := a.d.Auth.NeedsSetup(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"needsSetup": needs})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if err := validate.Username(req.Username); err != nil {
		writeError(w, r, err)
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeError(w, r, err)
		return
	}
	user, err := a.d.Auth.CreateInitialAdmin(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.LogAs(r.Context(), user.Username, audit.ActionSetup, "user", user.ID, nil)
	token, _, err := a.d.Auth.Login(r.Context(), req.Username, req.Password, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	writeJSON(w, http.StatusCreated, map[string]any{"user": userDTO{ID: user.ID, Username: user.Username, Role: user.Role}})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, r, auth.ErrInvalidCredentials)
		return
	}
	if len(req.Username) > 64 || len(req.Password) > auth.MaxPasswordLength*4 {
		writeError(w, r, auth.ErrInvalidCredentials)
		return
	}
	token, user, err := a.d.Auth.Login(r.Context(), req.Username, req.Password, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrTooManyAttempts) {
			a.d.Audit.LogAs(r.Context(), req.Username, audit.ActionLoginFailed, "user", "", map[string]string{"reason": err.Error()})
		}
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role})
	a.d.Audit.Log(ctx, audit.ActionLogin, "user", user.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO{ID: user.ID, Username: user.Username, Role: user.Role}})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromRequest(r)
	if p, err := a.d.Auth.Validate(r.Context(), token); err == nil {
		ctx := auth.WithPrincipal(r.Context(), p)
		a.d.Audit.Log(ctx, audit.ActionLogout, "user", p.UserID, nil)
	}
	if err := a.d.Auth.Logout(r.Context(), token); err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(""))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO{ID: p.UserID, Username: p.Username, Role: p.Role}})
}

type passwordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	if err := a.d.Auth.ChangePassword(r.Context(), p, req.CurrentPassword, req.NewPassword); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionPasswordChange, "user", p.UserID, nil)
	// All sessions were revoked; issue a fresh one so the current browser stays logged in.
	token, _, err := a.d.Auth.Login(r.Context(), p.Username, req.NewPassword, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	w.WriteHeader(http.StatusNoContent)
}
