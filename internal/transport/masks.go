package transport

import (
	"cadenceforge/internal/domain"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

func (a API) mask(w http.ResponseWriter, r *http.Request) {
	var input domain.Mask
	if err := decode(w, r, &input); err != nil {
		failure(w, err)
		return
	}
	rev, err := expected(r)
	if err != nil {
		failure(w, err)
		return
	}
	v, err := a.Service.SaveMask(r.PathValue("id"), rev, input)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(v.Revision))
	write(w, 201, v)
}

func (a API) maskHistory(w http.ResponseWriter, r *http.Request) {
	v, err := a.Service.MaskHistory(r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, v)
}

// audit runs the full-resolution pixel gate against an explicitly pinned mask
// revision. The revision query parameter is required: proxy review never
// resolves to "latest", so a later mask edit cannot change what was reviewed.
func (a API) audit(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || rev < 1 {
		failure(w, domain.Invalid("revision query must be a positive mask revision"))
		return
	}
	report, dir, _, err := a.Service.AuditMask(r.Context(), r.PathValue("id"), rev)
	if err != nil {
		failure(w, err)
		return
	}
	defer os.RemoveAll(dir)
	write(w, 200, report)
}

// auditEvidence streams one full-resolution evidence frame or the contact
// sheet for the explicitly pinned revision. It regenerates the evidence under
// the same frozen revision, then serves the requested file.
func (a API) auditEvidence(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil || rev < 1 {
		failure(w, domain.Invalid("revision query must be a positive mask revision"))
		return
	}
	name := r.URL.Query().Get("file")
	if name == "" || filepath.Base(name) != name {
		failure(w, domain.Invalid("file query must name one evidence file"))
		return
	}
	_, dir, files, err := a.Service.AuditMask(r.Context(), r.PathValue("id"), rev)
	if err != nil {
		failure(w, err)
		return
	}
	defer os.RemoveAll(dir)
	allowed := map[string]bool{}
	for _, f := range files {
		allowed[f] = true
	}
	if !allowed[name] {
		failure(w, domain.Missing("audit evidence", name))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(dir, name))
}

func (a API) evidence(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	if name == "coverage.json" {
		_, path, err := a.Service.Evidence(r.PathValue("id"), name)
		if err != nil {
			failure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		http.ServeFile(w, r, path)
		return
	}
	_, path, err := a.Service.Evidence(r.PathValue("id"), name)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, path)
}
