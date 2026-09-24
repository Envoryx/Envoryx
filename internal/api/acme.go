package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/acme"
	"github.com/envoryx/envoryx/internal/store"
)

func (a *API) acmeStatus(w http.ResponseWriter, r *http.Request) {
	if a.d.ACME == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "providers": acme.Providers, "providerList": acme.ProviderList})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "providers": acme.Providers, "providerList": acme.ProviderList, "status": a.d.ACME.Status()})
}

type acmeRequest struct {
	Provider string `json:"provider"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	// Credentials are the provider's fields (see providerList); empty secrets keep the
	// stored ones. Token is the Cloudflare token as older clients send it.
	Credentials map[string]string `json:"credentials"`
	Token       string            `json:"token"`
	Staging     bool              `json:"staging"`
	// UseAsBaseDomain also switches the project base domain to Domain.
	UseAsBaseDomain bool `json:"useAsBaseDomain"`
}

func (a *API) setACME(w http.ResponseWriter, r *http.Request) {
	if a.d.ACME == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	var req acmeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Token) > 512 {
		writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", "token too long"))
		return
	}
	if err := a.d.ACME.SetConfig(acme.Config{Provider: req.Provider, Domain: req.Domain, Email: req.Email, Credentials: req.Credentials, Token: req.Token, Staging: req.Staging}); err != nil {
		writeError(w, r, err)
		return
	}
	st := a.d.ACME.Status()
	if req.UseAsBaseDomain {
		if err := a.d.Projects.SetBaseDomain(r.Context(), st.Domain); err != nil {
			writeError(w, r, err)
			return
		}
		a.invalidateProxy()
	}
	a.d.Audit.Log(r.Context(), "settings.changed", "settings", "", map[string]any{"acme": map[string]any{"provider": st.Provider, "domain": st.Domain, "staging": st.Staging}})
	a.acmeStatus(w, r)
}

func (a *API) clearACME(w http.ResponseWriter, r *http.Request) {
	if a.d.ACME == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	if err := a.d.ACME.Clear(); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), "settings.changed", "settings", "", map[string]any{"acme": "removed"})
	a.acmeStatus(w, r)
}

func (a *API) issueACME(w http.ResponseWriter, r *http.Request) {
	if a.d.ACME == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	if !a.d.ACME.Status().Configured {
		writeError(w, r, newError(http.StatusConflict, "conflict", "Let's Encrypt is not configured"))
		return
	}
	a.d.ACME.Trigger()
	w.WriteHeader(http.StatusAccepted)
}
