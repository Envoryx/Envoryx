package api

import (
	"fmt"
	"net/http"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/store"
)

// listBranchEnvironments returns a project's branch settings, its environments and the
// outcome of the last poll of its repository: GET /projects/{id}/branches.
func (a *API) listBranchEnvironments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	views, err := a.d.Projects.BranchEnvironments(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	parent, err := a.d.Projects.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	envs := make([]projectDTO, 0, len(views))
	for _, v := range views {
		envs = append(envs, a.project(r, v))
	}
	out := map[string]any{"settings": parent.Project.Branches, "environments": envs}
	if poll, ok := a.d.Projects.LastBranchPoll(id); ok {
		out["lastPoll"] = poll
	}
	writeJSON(w, http.StatusOK, out)
}

// setBranchSettings stores the branch settings: PUT /projects/{id}/branches/settings.
func (a *API) setBranchSettings(w http.ResponseWriter, r *http.Request) {
	var req store.BranchSettings
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	settings, err := a.d.Projects.SetBranchSettings(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

// remoteBranches lists the branches of the project's repository with their environments:
// GET /projects/{id}/branches/remote. It asks the remote, which takes a moment.
func (a *API) remoteBranches(w http.ResponseWriter, r *http.Request) {
	branches, err := a.d.Projects.RemoteBranches(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches})
}

// createBranchEnvironment makes a branch environment: POST /projects/{id}/branches with
// {"branch": "feature/x"}. Like a duplicate it creates a project.
func (a *API) createBranchEnvironment(w http.ResponseWriter, r *http.Request) {
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" && p.Restricted() {
		writeError(w, r, fmt.Errorf("%w: this token is limited to particular projects and cannot create new ones", auth.ErrForbidden))
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.CreateBranchEnvironment(r.Context(), r.PathValue("id"), req.Branch)
	a.invalidateProxy()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": a.project(r, view)})
}

// deployBranchEnvironment pulls a branch environment and runs the deploy commands:
// POST /projects/{id}/deploy, {"pull": false} to only run the commands.
func (a *API) deployBranchEnvironment(w http.ResponseWriter, r *http.Request) {
	req := struct {
		Pull *bool `json:"pull"`
	}{}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, r, err)
			return
		}
	}
	pull := req.Pull == nil || *req.Pull
	view, err := a.d.Projects.Deploy(r.Context(), r.PathValue("id"), pull)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}
