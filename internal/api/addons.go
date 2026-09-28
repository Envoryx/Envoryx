package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/envoryx/envoryx/internal/addon"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/validate"
)

// installedAddonDTO is an installed addon with the projects running it.
type installedAddonDTO struct {
	addon.Installed
	Projects []string `json:"projects"`
}

// listAddons returns the installed addons and the examples Envoryx ships.
func (a *API) listAddons(w http.ResponseWriter, r *http.Request) {
	list, err := a.d.Projects.Addons().List()
	if err != nil {
		writeError(w, r, err)
		return
	}
	usage, err := a.d.Projects.AddonUsage(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]installedAddonDTO, 0, len(list))
	for _, inst := range list {
		users := usage[inst.Name]
		if users == nil {
			users = []string{}
		}
		out = append(out, installedAddonDTO{Installed: inst, Projects: users})
	}
	writeJSON(w, http.StatusOK, map[string]any{"addons": out, "examples": addon.Examples()})
}

// getAddon returns an installed addon with its file.
func (a *API) getAddon(w http.ResponseWriter, r *http.Request) {
	d, raw, err := a.d.Projects.Addons().Get(r.PathValue("name"))
	if err != nil {
		writeError(w, r, addonError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addon": d, "source": string(raw)})
}

// installAddon installs an addon file: {"source": "<yaml>"} or {"url": "https://…"}.
func (a *API) installAddon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		URL    string `json:"url"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var (
		d   addon.Definition
		err error
	)
	switch {
	case req.Source != "" && req.URL != "":
		err = fmt.Errorf("%w: give the file or a URL, not both", validate.ErrInvalid)
	case req.URL != "":
		d, err = a.d.Projects.InstallAddonFromURL(r.Context(), req.URL)
	case req.Source != "":
		d, err = a.d.Projects.InstallAddon(r.Context(), []byte(req.Source))
	default:
		err = fmt.Errorf("%w: the addon file or a URL is required", validate.ErrInvalid)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"addon": d})
}

// deleteAddon removes an addon file no project uses.
func (a *API) deleteAddon(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.DeleteAddon(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectAddons describes a project's addons. Secret credentials are left empty for
// callers who may only read the project.
func (a *API) projectAddons(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list, err := a.d.Projects.ProjectAddons(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if p, _ := auth.PrincipalFrom(r.Context()); !p.ScopeFor(id).Covers(auth.ScopeOperate) {
		for i := range list {
			for j := range list[i].Credentials {
				if list[i].Credentials[j].Secret {
					list[i].Credentials[j].Value = ""
				}
			}
		}
	}
	// What can be added: the installed addons the project does not run yet.
	type availableDTO struct {
		Name        string   `json:"name"`
		Title       string   `json:"title"`
		Description string   `json:"description,omitempty"`
		Versions    []string `json:"versions"`
		PublishPort bool     `json:"publishPort,omitempty"`
		WebUI       bool     `json:"webUI,omitempty"`
		HasVolumes  bool     `json:"hasVolumes,omitempty"`
	}
	have := map[string]bool{}
	for _, in := range list {
		have[in.Name] = true
	}
	available := []availableDTO{}
	if installed, err := a.d.Projects.Addons().List(); err == nil {
		for _, in := range installed {
			if in.Error != "" || have[in.Name] {
				continue
			}
			av := availableDTO{Name: in.Name, Title: in.Title, Description: in.Description, PublishPort: in.PublishPort, WebUI: in.WebUI, HasVolumes: len(in.Volumes) > 0, Versions: []string{}}
			for _, v := range in.Versions {
				av.Versions = append(av.Versions, v.Version)
			}
			available = append(available, av)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"addons": list, "available": available})
}

// setProjectAddon adds, changes or removes an addon of a project:
// PUT /projects/{id}/addons/{name} {"enabled", "version", "exposePort", "removeData"}.
func (a *API) setProjectAddon(w http.ResponseWriter, r *http.Request) {
	var req project.AddonUpdate
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := a.d.Projects.Update(r.Context(), r.PathValue("id"), project.UpdateRequest{Addons: map[string]project.AddonUpdate{r.PathValue("name"): req}})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.invalidateProxy()
	writeJSON(w, http.StatusOK, map[string]any{"project": a.project(r, view)})
}

func addonError(err error) error {
	if errors.Is(err, addon.ErrNotFound) {
		return fmt.Errorf("%w: %v", project.ErrNotFound, err)
	}
	return err
}
