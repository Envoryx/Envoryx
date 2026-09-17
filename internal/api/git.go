package api

import (
	"net/http"

	"github.com/seramos/staqio/internal/project"
)

func (a *API) gitStatus(w http.ResponseWriter, r *http.Request) {
	st, err := a.d.Projects.GitStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"git": st})
}

type gitRequestDTO struct {
	URL      string `json:"url"`
	Branch   string `json:"branch"`
	Username string `json:"username"`
	// Token is write-only; omit (null) to keep the stored token, "" to clear it.
	Token *string `json:"token"`
}

func (d gitRequestDTO) toDomain() project.GitRequest {
	req := project.GitRequest{URL: d.URL, Branch: d.Branch, Username: d.Username, KeepToken: d.Token == nil}
	if d.Token != nil {
		req.Token = *d.Token
	}
	return req
}

func (a *API) gitSet(w http.ResponseWriter, r *http.Request) {
	var req gitRequestDTO
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	st, err := a.d.Projects.SetGit(r.Context(), r.PathValue("id"), req.toDomain())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"git": st})
}

func (a *API) gitClone(w http.ResponseWriter, r *http.Request) {
	res, err := a.d.Projects.Clone(r.Context(), r.PathValue("id"))
	a.gitResult(w, r, res, err)
}

func (a *API) gitPull(w http.ResponseWriter, r *http.Request) {
	res, err := a.d.Projects.Pull(r.Context(), r.PathValue("id"))
	a.gitResult(w, r, res, err)
}

type gitCheckoutRequest struct {
	Branch string `json:"branch"`
}

func (a *API) gitCheckout(w http.ResponseWriter, r *http.Request) {
	var req gitCheckoutRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := a.d.Projects.Checkout(r.Context(), r.PathValue("id"), req.Branch)
	a.gitResult(w, r, res, err)
}

// gitResult reports failed git commands with their output so the UI can show it, using
// 409 like other operational conflicts.
func (a *API) gitResult(w http.ResponseWriter, r *http.Request, res project.GitResult, err error) {
	if err != nil {
		if res.ExitCode != 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":  ErrorDetail{Code: "git_failed", Message: err.Error()},
				"result": res,
			})
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (a *API) deployKey(w http.ResponseWriter, r *http.Request) {
	key, err := a.d.Projects.DeployKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": key})
}

func (a *API) deployKeyRegenerate(w http.ResponseWriter, r *http.Request) {
	key, err := a.d.Projects.RegenerateDeployKey(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": key})
}
