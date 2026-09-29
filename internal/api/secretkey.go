package api

import "net/http"

// secretKey describes the key that seals the secrets at rest.
func (a *API) secretKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"secretKey": a.d.Projects.SecretKeyInfo()})
}

// revealSecretKey returns the key itself, for the admin to keep a copy (audited).
func (a *API) revealSecretKey(w http.ResponseWriter, r *http.Request) {
	key, err := a.d.Projects.RevealSecretKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

// rotateSecretKey replaces the key file's key and reseals every secret.
func (a *API) rotateSecretKey(w http.ResponseWriter, r *http.Request) {
	info, rep, err := a.d.Projects.RotateSecretKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secretKey": info, "resealed": rep})
}
