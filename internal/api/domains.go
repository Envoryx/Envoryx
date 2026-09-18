package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

type domainDTO struct {
	ID        string    `json:"id,omitempty"`
	Hostname  string    `json:"hostname"`
	Default   bool      `json:"default"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// projectURLs builds the URLs a project is reachable at: domains through the proxy (when
// published) and the direct port.
func (a *API) projectHosts(r *http.Request, p store.Project) ([]domainDTO, error) {
	base := a.d.Projects.BaseDomain(r.Context())
	out := []domainDTO{{Hostname: project.DefaultHostname(p.Slug, base), Default: true}}
	extra, err := a.d.Store.Domains.ListByProject(r.Context(), p.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range extra {
		out = append(out, domainDTO{ID: d.ID, Hostname: d.Hostname, CreatedAt: d.CreatedAt})
	}
	return out, nil
}

func (a *API) listDomains(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validate.UUID(id); err != nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	p, err := a.d.Store.Projects.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	hosts, err := a.projectHosts(r, p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": hosts, "proxy": a.proxyDTO()})
}

type addDomainRequest struct {
	Hostname string `json:"hostname"`
}

func (a *API) addDomain(w http.ResponseWriter, r *http.Request) {
	var req addDomainRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := a.d.Projects.AddDomain(r.Context(), r.PathValue("id"), strings.TrimSpace(req.Hostname))
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusCreated, map[string]any{"domain": domainDTO{ID: d.ID, Hostname: d.Hostname, CreatedAt: d.CreatedAt}})
}

func (a *API) removeDomain(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.RemoveDomain(r.Context(), r.PathValue("id"), r.PathValue("domain")); err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) invalidateProxy() {
	if a.d.Proxy != nil && a.d.Proxy.Invalidate != nil {
		a.d.Proxy.Invalidate()
	}
}

type proxyDTO struct {
	Enabled   bool   `json:"enabled"`
	HTTPPort  int    `json:"httpPort"`
	HTTPSPort int    `json:"httpsPort"`
	InDocker  bool   `json:"inDocker"`
	TLS       bool   `json:"tls"`
	Address   string `json:"address,omitempty"`
}

func (a *API) proxyDTO() proxyDTO {
	if a.d.Proxy == nil {
		return proxyDTO{}
	}
	return proxyDTO{Enabled: a.d.Proxy.Enabled, HTTPPort: a.d.Proxy.HTTPPort, HTTPSPort: a.d.Proxy.HTTPSPort, InDocker: a.d.Proxy.InDocker, TLS: a.d.Certs != nil && a.d.Proxy.HTTPSPort > 0, Address: a.d.Proxy.Address}
}

func (a *API) tlsInfo(w http.ResponseWriter, r *http.Request) {
	if a.d.Certs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "proxy": a.proxyDTO()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "ca": a.d.Certs.Info(), "proxy": a.proxyDTO(), "baseDomain": a.d.Projects.BaseDomain(r.Context()), "forceHttps": a.d.Projects.ForceHTTPS(r.Context())})
}

func (a *API) downloadCA(w http.ResponseWriter, r *http.Request) {
	if a.d.Certs == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="envoryx-ca.crt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(a.d.Certs.CAPEM())
}

type customCertRequest struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

func (a *API) setCustomCert(w http.ResponseWriter, r *http.Request) {
	if a.d.Certs == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	var req customCertRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.Certificate) > 64<<10 || len(req.Key) > 64<<10 {
		writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", "certificate or key too large"))
		return
	}
	if err := a.d.Certs.SetCustom([]byte(req.Certificate), []byte(req.Key)); err != nil {
		writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", err.Error()))
		return
	}
	a.d.Audit.Log(r.Context(), "settings.changed", "settings", "", map[string]any{"customCertificate": "set"})
	a.tlsInfo(w, r)
}

func (a *API) clearCustomCert(w http.ResponseWriter, r *http.Request) {
	if a.d.Certs == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	if err := a.d.Certs.SetCustom(nil, nil); err != nil {
		writeError(w, r, err)
		return
	}
	a.d.Audit.Log(r.Context(), "settings.changed", "settings", "", map[string]any{"customCertificate": "removed"})
	a.tlsInfo(w, r)
}
