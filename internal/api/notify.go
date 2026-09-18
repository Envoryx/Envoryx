package api

import (
	"net/http"

	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
)

func (a *API) notificationStatus(w http.ResponseWriter, r *http.Request) {
	if a.d.Notify == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": a.d.Notify.Status(), "providers": notify.Providers, "kinds": notify.Kinds})
}

func (a *API) setNotifications(w http.ResponseWriter, r *http.Request) {
	if a.d.Notify == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	var cfg notify.Config
	if err := decodeJSON(w, r, &cfg); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Notify.SetConfig(cfg); err != nil {
		writeError(w, r, newError(http.StatusUnprocessableEntity, "validation_failed", err.Error()))
		return
	}
	st := a.d.Notify.Status()
	a.d.Audit.Log(r.Context(), "settings.changed", "settings", "", map[string]any{"notifications": map[string]any{"enabled": st.Config.Enabled, "provider": st.Config.Provider}})
	a.notificationStatus(w, r)
}

func (a *API) testNotifications(w http.ResponseWriter, r *http.Request) {
	if a.d.Notify == nil {
		writeError(w, r, store.ErrNotFound)
		return
	}
	var cfg notify.Config
	if err := decodeJSON(w, r, &cfg); err != nil {
		writeError(w, r, err)
		return
	}
	if err := a.d.Notify.Test(r.Context(), cfg); err != nil {
		writeError(w, r, newError(http.StatusBadGateway, "delivery_failed", err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
