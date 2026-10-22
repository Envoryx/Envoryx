package api

import (
	"errors"
	"net/http"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/proxy"
)

var (
	errGateNeedsSession = newError(http.StatusForbidden, "forbidden", "only a browser session can open a page behind the Envoryx sign-in")
	errNotGated         = newError(http.StatusNotFound, "not_gated", "this address isn't behind the Envoryx sign-in")
)

// gate lets the signed-in user into a host behind the Envoryx sign-in (proxy.Gate), such
// as an addon's web UI: POST /auth/gate {"to": "<the address asked for>"} answers the
// address that hands the proxy a one-time code for it. The UI's /gate page calls it.
func (a *API) gate(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if p.TokenName != "" || p.SessionID == "" {
		writeError(w, r, errGateNeedsSession)
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if a.d.Gate == nil {
		writeError(w, r, errNotGated)
		return
	}
	u, target, err := a.d.Gate.Resolve(r.Context(), body.To)
	if errors.Is(err, proxy.ErrNotGated) {
		writeError(w, r, errNotGated)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := p.Require(auth.Scope(target.Login), target.ProjectID); err != nil {
		writeError(w, r, err)
		return
	}
	next, err := a.d.Gate.Issue(r.Context(), p.SessionID, u, target)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": next, "name": target.ProjectName})
}
