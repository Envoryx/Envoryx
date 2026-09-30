package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/auth"
)

// errSecretKeyNeedsSession keeps a leaked API token from reading or replacing the key
// that decrypts every stored secret; only an admin's browser session may.
var errSecretKeyNeedsSession = newError(http.StatusForbidden, "forbidden", "API tokens cannot reveal or rotate the secret key; sign in with a browser session")

// sessionOnly refuses token principals with err; the route itself sets the level.
func sessionOnly(w http.ResponseWriter, r *http.Request, err *apiError) bool {
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" {
		writeError(w, r, err)
		return false
	}
	return true
}

// secretKey describes the key that seals the secrets at rest.
func (a *API) secretKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"secretKey": a.d.Projects.SecretKeyInfo()})
}

// revealSecretKey returns the key itself, for the admin to keep a copy (audited).
func (a *API) revealSecretKey(w http.ResponseWriter, r *http.Request) {
	if !sessionOnly(w, r, errSecretKeyNeedsSession) {
		return
	}
	key, err := a.d.Projects.RevealSecretKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

// rotateSecretKey replaces the key file's key and reseals every secret.
func (a *API) rotateSecretKey(w http.ResponseWriter, r *http.Request) {
	if !sessionOnly(w, r, errSecretKeyNeedsSession) {
		return
	}
	info, rep, err := a.d.Projects.RotateSecretKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secretKey": info, "resealed": rep})
}
