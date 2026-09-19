package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

type tokenDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func toToken(t store.APIToken) tokenDTO {
	return tokenDTO{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Auth.ListAPITokens(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]tokenDTO, 0, len(list))
	for _, t := range list {
		out = append(out, toToken(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out, "mcpUrl": a.mcpURL(r)})
}

type createTokenRequest struct {
	Name string `json:"name"`
}

// errTokenManagesTokens keeps a leaked API token from minting or revoking tokens; only a
// browser session may do that.
var errTokenManagesTokens = newError(http.StatusForbidden, "forbidden", "API tokens cannot manage tokens; sign in with a browser session")

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" {
		writeError(w, r, errTokenManagesTokens)
		return
	}
	token, t, err := a.d.Auth.CreateAPIToken(r.Context(), p, req.Name)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidTokenName) {
			writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", err.Error()))
			return
		}
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), "token.created", "token", t.ID, map[string]any{"name": t.Name})
	// The plain token is returned exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"token": toToken(t), "secret": token, "mcpUrl": a.mcpURL(r)})
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" {
		writeError(w, r, errTokenManagesTokens)
		return
	}
	id := r.PathValue("id")
	if err := a.d.Auth.RevokeAPIToken(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), "token.revoked", "token", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// mcpURL is the MCP endpoint as seen by the current request (scheme/host of the UI).
func (a *API) mcpURL(r *http.Request) string {
	if a.d.MCP == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp"
}
