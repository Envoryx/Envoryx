package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/validate"
)

// maxSiteUpload bounds an uploaded website (archive and dump together).
const maxSiteUpload = 20 << 30

// uploadSite stages a website for import: POST /site-imports with the multipart fields
// "site" (ZIP or tar archive) and optionally "database" (.sql or .sql.gz). The answer
// is the analysis the project wizard is filled from.
func (a *API) uploadSite(w http.ResponseWriter, r *http.Request) {
	if p, _ := auth.PrincipalFrom(r.Context()); p.TokenName != "" && p.Restricted() {
		writeError(w, r, fmt.Errorf("%w: this token is limited to particular projects and cannot create new ones", auth.ErrForbidden))
		return
	}
	// A website takes longer to upload than the server's read timeout allows a request.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(6 * time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, maxSiteUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, fmt.Errorf("%w: expected a multipart upload", validate.ErrInvalid))
		return
	}
	up, err := a.d.Projects.BeginSiteImport()
	if err != nil {
		writeError(w, r, err)
		return
	}
	fail := func(err error) {
		up.Abort()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = fmt.Errorf("%w: the upload exceeds %d GiB", validate.ErrInvalid, tooLarge.Limit>>30)
		}
		writeError(w, r, err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(fmt.Errorf("%w: %w", validate.ErrInvalid, err))
			return
		}
		switch part.FormName() {
		case "site":
			err = up.Site(part, part.FileName())
		case "database":
			if part.FileName() != "" {
				err = up.Dump(part, part.FileName())
			}
		}
		_ = part.Close()
		if err != nil {
			fail(err)
			return
		}
	}
	staged, err := up.Finish(a.d.Projects.SiteImportOptions())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"import": staged})
}

func (a *API) getSiteImport(w http.ResponseWriter, r *http.Request) {
	staged, err := a.d.Projects.SiteImport(r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"import": staged})
}

func (a *API) deleteSiteImport(w http.ResponseWriter, r *http.Request) {
	if err := a.d.Projects.DiscardSiteImport(r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
