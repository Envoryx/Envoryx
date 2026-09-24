package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/validate"
)

// metricRanges are the time ranges the charts offer.
var metricRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour, "365d": 365 * 24 * time.Hour,
}

func metricRange(r *http.Request) (time.Duration, error) {
	v := r.URL.Query().Get("range")
	if v == "" {
		return 24 * time.Hour, nil
	}
	d, ok := metricRanges[v]
	if !ok {
		return 0, fmt.Errorf("%w: range must be one of 1h, 6h, 24h, 7d, 30d, 90d, 365d", validate.ErrInvalid)
	}
	return d, nil
}

// projectMetrics returns a project's resource history: GET /projects/{id}/metrics?range=.
func (a *API) projectMetrics(w http.ResponseWriter, r *http.Request) {
	rng, err := metricRange(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	m, err := a.d.Projects.ProjectMetrics(r.Context(), r.PathValue("id"), rng)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": m})
}

// metricsOverview compares the projects: GET /metrics/overview?range=.
func (a *API) metricsOverview(w http.ResponseWriter, r *http.Request) {
	rng, err := metricRange(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	ov, err := a.d.Projects.MetricsOverview(r.Context(), rng)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overview": ov})
}

// clearMetrics deletes the resource history: DELETE /settings/metrics.
func (a *API) clearMetrics(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.ClearMetrics(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": a.d.Projects.MetricsInfo(r.Context())})
}
