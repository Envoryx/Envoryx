package api

import (
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/plan"
)

// planUsage is how much of the plan the instance uses.
type planUsage struct {
	Projects  int   `json:"projects"`
	Users     int   `json:"users"`
	DiskBytes int64 `json:"diskBytes"`
	// DiskMeasuredAt is when the disk space was last measured; zero while the plan
	// doesn't limit it.
	DiskMeasuredAt time.Time `json:"diskMeasuredAt,omitzero"`
}

// planStatus returns the hoster's plan of a managed instance (null for an instance
// without one) and how much of it is used.
func (a *API) planStatus(w http.ResponseWriter, r *http.Request) {
	pl := a.d.Projects.Plan()
	if pl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"plan": nil, "fleet": a.d.Fleet.Info()})
		return
	}
	var u planUsage
	var err error
	if u.Projects, err = a.d.Store.Projects.Count(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	if u.Users, err = a.d.Store.Users.CountActive(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	u.DiskBytes, u.DiskMeasuredAt = a.d.Projects.DiskUsage()
	writeJSON(w, http.StatusOK, map[string]any{"plan": planDTO(pl), "usage": u, "fleet": a.d.Fleet.Info()})
}

// planDTO is a plan with its lists never null.
func planDTO(p *plan.Plan) map[string]any {
	runtimes, disabled := p.Runtimes, p.Disabled
	if runtimes == nil {
		runtimes = []string{}
	}
	if disabled == nil {
		disabled = []plan.Feature{}
	}
	return map[string]any{
		"name": p.Name, "limits": p.Limits, "runtimes": runtimes, "disabled": disabled, "lockedSettings": p.LockedKeys(),
	}
}
