package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
	"golang.org/x/crypto/ssh"
)

// errUsersNeedSession keeps a leaked API token from managing accounts; only an admin's
// browser session may.
var errUsersNeedSession = newError(http.StatusForbidden, "forbidden", "API tokens cannot manage users; sign in with a browser session")

// userAdminDTO is a user as the user management shows it.
type userAdminDTO struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	// Invited is set while the user has not set a password through the invitation yet;
	// InviteExpiresAt is when the open invitation (or password reset) ends.
	Invited         bool       `json:"invited"`
	InviteExpiresAt *time.Time `json:"inviteExpiresAt,omitempty"`
	// SSO is set for a user linked to an OpenID Connect account.
	SSO          bool              `json:"sso"`
	ProjectRoles map[string]string `json:"projectRoles"`
	CreatedAt    time.Time         `json:"createdAt"`
}

func (a *API) userAdmin(r *http.Request, u store.User) userAdminDTO {
	d := userAdminDTO{ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled, Invited: u.PasswordHash == "" && u.OIDCSubject == "", SSO: u.OIDCSubject != "", CreatedAt: u.CreatedAt, ProjectRoles: map[string]string{}}
	if u.InviteHash != "" {
		exp := u.InviteExpiresAt
		d.InviteExpiresAt = &exp
	}
	if roles, err := a.d.Store.Roles.ByUser(r.Context(), u.ID); err == nil {
		d.ProjectRoles = roles
	}
	return d
}

// inviteURL is the page an invitation token opens, on the address the admin used.
func inviteURL(r *http.Request, token string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/invite/" + token
}

// sessionAdmin refuses token principals; the route itself requires admin.
func sessionAdmin(w http.ResponseWriter, r *http.Request) bool {
	return sessionOnly(w, r, errUsersNeedSession)
}

// listUsers returns every user: GET /users.
func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	users, err := a.d.Store.Users.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]userAdminDTO, 0, len(users))
	for _, u := range users {
		out = append(out, a.userAdmin(r, u))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

type inviteUserRequest struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	// ProjectRoles are roles in particular projects, set right away.
	ProjectRoles map[string]string `json:"projectRoles"`
}

// inviteUser creates a user and returns the invitation link: POST /users.
func (a *API) inviteUser(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	var req inviteUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Role == "" {
		req.Role = string(auth.RoleViewer)
	}
	token, u, err := a.d.Auth.InviteUser(r.Context(), strings.TrimSpace(req.Username), auth.Role(req.Role))
	if err != nil {
		writeError(w, r, err)
		return
	}
	for project, role := range req.ProjectRoles {
		if err := a.d.Auth.SetProjectRole(r.Context(), u.ID, project, auth.Role(role)); err != nil {
			_ = a.d.Auth.DeleteUser(r.Context(), u.ID)
			writeError(w, r, err)
			return
		}
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserInvited, "user", u.ID, map[string]any{"username": u.Username, "role": u.Role, "projectRoles": req.ProjectRoles})
	writeJSON(w, http.StatusCreated, map[string]any{"user": a.userAdmin(r, u), "inviteUrl": inviteURL(r, token), "expiresAt": u.InviteExpiresAt})
}

type updateUserRequest struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// updateUser changes a user's role or disables them: PATCH /users/{id}.
func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	var req updateUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	u, err := a.d.Store.Users.ByID(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	changes := map[string]any{"username": u.Username}
	if req.Role != nil && *req.Role != u.Role {
		if u, err = a.d.Auth.SetUserRole(r.Context(), id, auth.Role(*req.Role)); err != nil {
			writeError(w, r, err)
			return
		}
		changes["role"] = u.Role
	}
	if req.Disabled != nil && *req.Disabled != u.Disabled {
		if p, _ := auth.PrincipalFrom(r.Context()); *req.Disabled && p.UserID == id {
			writeError(w, r, newError(http.StatusConflict, "conflict", "you cannot disable yourself"))
			return
		}
		if u, err = a.d.Auth.SetUserDisabled(r.Context(), id, *req.Disabled); err != nil {
			writeError(w, r, err)
			return
		}
		changes["disabled"] = u.Disabled
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserUpdated, "user", id, changes)
	writeJSON(w, http.StatusOK, map[string]any{"user": a.userAdmin(r, u)})
}

// renewInvite hands out a new invitation link, which also resets a forgotten password:
// POST /users/{id}/invite.
func (a *API) renewInvite(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	token, u, err := a.d.Auth.RenewInvite(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserInvited, "user", u.ID, map[string]any{"username": u.Username, "renewed": true})
	writeJSON(w, http.StatusOK, map[string]any{"user": a.userAdmin(r, u), "inviteUrl": inviteURL(r, token), "expiresAt": u.InviteExpiresAt})
}

// deleteUser removes a user: DELETE /users/{id}.
func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if p, _ := auth.PrincipalFrom(r.Context()); p.UserID == id {
		writeError(w, r, newError(http.StatusConflict, "conflict", "you cannot delete yourself"))
		return
	}
	u, err := a.d.Store.Users.ByID(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.DeleteUser(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserDeleted, "user", id, map[string]any{"username": u.Username})
	w.WriteHeader(http.StatusNoContent)
}

// setProjectRole gives a user a role in one project, "" removes it:
// PUT /users/{id}/projects/{project} with {"role": "developer"}.
func (a *API) setProjectRole(w http.ResponseWriter, r *http.Request) {
	if !sessionAdmin(w, r) {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id, projectID := r.PathValue("id"), r.PathValue("project")
	if _, err := a.d.Projects.Get(r.Context(), projectID); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Auth.SetProjectRole(r.Context(), id, projectID, auth.Role(req.Role)); err != nil {
		writeError(w, r, err)
		return
	}
	u, err := a.d.Store.Users.ByID(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserUpdated, "user", id, map[string]any{"username": u.Username, "project": projectID, "projectRole": req.Role})
	writeJSON(w, http.StatusOK, map[string]any{"user": a.userAdmin(r, u)})
}

// invitation tells the invitation page whose link it is: GET /invites/{token}. Public.
func (a *API) invitation(w http.ResponseWriter, r *http.Request) {
	u, err := a.d.Auth.Invitation(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": u.Username, "expiresAt": u.InviteExpiresAt, "reset": u.PasswordHash != ""})
}

// acceptInvitation sets the password and signs the user in: POST /invites/{token} with
// {"password": "…"}. Public; the token is the credential.
func (a *API) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	u, err := a.d.Auth.AcceptInvite(r.Context(), r.PathValue("token"), req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.LogAs(r.Context(), u.Username, audit.ActionUserJoined, "user", u.ID, nil)
	token, err := a.d.Auth.StartSession(r.Context(), u, auth.ClientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.d.Auth.Cookie(token))
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO{ID: u.ID, Username: u.Username, Role: u.Role}})
}

// mySSHKeys returns the caller's own public keys for the SSH server: GET /auth/ssh-keys.
func (a *API) mySSHKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" {
		writeError(w, r, errUsersNeedSession)
		return
	}
	u, err := a.d.Store.Users.ByID(r.Context(), p.UserID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": u.SSHKeys})
}

// setMySSHKeys stores them: PUT /auth/ssh-keys with {"keys": "ssh-ed25519 AAAA… me@laptop"}.
// Every line that is not empty or a comment must be a public key.
func (a *API) setMySSHKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" {
		writeError(w, r, errUsersNeedSession)
		return
	}
	var req struct {
		Keys string `json:"keys"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Keys) > 64<<10 {
		writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", "too many keys"))
		return
	}
	for i, line := range strings.Split(req.Keys, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
			writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", fmt.Sprintf("line %d is not a public key", i+1)))
			return
		}
	}
	if err := a.d.Store.Users.SetSSHKeys(r.Context(), p.UserID, req.Keys); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), audit.ActionUserUpdated, "user", p.UserID, map[string]any{"username": p.Username, "sshKeys": true})
	writeJSON(w, http.StatusOK, map[string]any{"keys": req.Keys})
}
