package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/uug-ai/gist/internal/render"
	"github.com/uug-ai/gist/internal/store"
)

// The JSON API follows GitHub's gist API where it can: files is an object
// keyed by filename, and PATCH can rename (filename) and delete (null) files.

type apiFileIn struct {
	Filename *string `json:"filename"`
	Content  *string `json:"content"`
}

type apiGistIn struct {
	Description *string               `json:"description"`
	Files       map[string]*apiFileIn `json:"files"`
	ExpiresIn   string                `json:"expires_in"`
	// Public lists the gist on Discover; false or absent keeps it secret.
	Public *bool `json:"public"`
}

type apiFileOut struct {
	Filename string `json:"filename"`
	Language string `json:"language"`
	Size     int    `json:"size"`
	RawURL   string `json:"raw_url"`
	Content  string `json:"content"`
}

type apiGistOut struct {
	ID          string                `json:"id"`
	URL         string                `json:"url"`
	HTMLURL     string                `json:"html_url"`
	Description string                `json:"description"`
	Revision    int                   `json:"revision"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	ExpiresAt   *time.Time            `json:"expires_at"`
	ForkOf      string                `json:"fork_of,omitempty"`
	Public      bool                  `json:"public"`
	Files       map[string]apiFileOut `json:"files"`
	EditKey     string                `json:"edit_key,omitempty"`
}

func (s *Server) apiOut(r *http.Request, m *store.Meta, rev *store.Revision) apiGistOut {
	base := s.baseURL(r)
	out := apiGistOut{
		ID:          m.ID,
		URL:         base + "/api/v1/gists/" + m.ID,
		HTMLURL:     base + gistPath(m.ID),
		Description: rev.Description,
		Revision:    rev.Number,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		ExpiresAt:   m.ExpiresAt,
		ForkOf:      m.ForkOf,
		Public:      m.Public,
		Files:       map[string]apiFileOut{},
	}
	for _, f := range rev.Files {
		out.Files[f.Name] = apiFileOut{
			Filename: f.Name,
			Language: render.Language(f.Name, f.Content),
			Size:     len(f.Content),
			RawURL:   base + rawPath(m.ID, rev.Number, true, f.Name),
			Content:  f.Content,
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *Server) apiStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "Not found")
	case errors.Is(err, store.ErrQuota):
		apiError(w, http.StatusInsufficientStorage, "Storage quota reached")
	case errors.Is(err, store.ErrTooManyRevisions):
		apiError(w, http.StatusConflict, "Too many revisions; fork the gist to keep editing")
	case store.IsValidation(err):
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		s.log.Error("api", "error", err)
		apiError(w, http.StatusInternalServerError, "Internal error")
	}
}

func (s *Server) apiAllow(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter == nil || s.limiter.allow(s.clientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	apiError(w, http.StatusTooManyRequests, "Rate limit exceeded")
	return false
}

func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.Limits.MaxBytes)*2+64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request) {
	if !s.apiAllow(w, r) {
		return
	}
	var in apiGistIn
	if !s.readJSON(w, r, &in) {
		return
	}
	expiresIn, err := parseExpiry(in.ExpiresIn)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var files []store.File
	for name, f := range in.Files {
		if f == nil || f.Content == nil {
			apiError(w, http.StatusUnprocessableEntity, "Every file needs content")
			return
		}
		files = append(files, store.File{Name: name, Content: *f.Content})
	}
	desc := ""
	if in.Description != nil {
		desc = *in.Description
	}
	desc, files, err = store.Normalize(desc, files, s.cfg.Limits)
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	m, token, err := s.store.Create(store.CreateInput{
		Description: desc, Files: files, ExpiresIn: expiresIn, Public: in.Public != nil && *in.Public,
	})
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	out := s.apiOut(r, m, &store.Revision{Number: 1, Description: desc, Files: files})
	out.EditKey = token
	w.Header().Set("Location", out.URL)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) apiGet(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	rev, err := s.store.Revision(m.ID, m.Latest())
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.apiOut(r, m, rev))
}

func (s *Server) apiOwner(w http.ResponseWriter, r *http.Request) (*store.Meta, bool) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		s.apiStoreError(w, err)
		return nil, false
	}
	if !s.authorized(m, ownerToken(r, m.ID)) {
		apiError(w, http.StatusForbidden, "Send the gist's edit key as Authorization: Bearer <key>")
		return nil, false
	}
	return m, true
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.apiAllow(w, r) {
		return
	}
	m, ok := s.apiOwner(w, r)
	if !ok {
		return
	}
	var in apiGistIn
	if !s.readJSON(w, r, &in) {
		return
	}
	if in.ExpiresIn != "" {
		apiError(w, http.StatusUnprocessableEntity, "expires_in can only be set when creating a gist")
		return
	}
	rev, err := s.store.Revision(m.ID, m.Latest())
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	desc := rev.Description
	if in.Description != nil {
		desc = *in.Description
	}
	files := map[string]string{}
	order := []string{}
	for _, f := range rev.Files {
		files[f.Name] = f.Content
		order = append(order, f.Name)
	}
	for name, f := range in.Files {
		content, exists := files[name]
		switch {
		case f == nil: // delete
			if !exists {
				apiError(w, http.StatusUnprocessableEntity, "No file called "+name+" to delete")
				return
			}
			delete(files, name)
		default:
			if f.Content != nil {
				content = *f.Content
			} else if !exists {
				apiError(w, http.StatusUnprocessableEntity, "New file "+name+" needs content")
				return
			}
			newName := name
			if f.Filename != nil && *f.Filename != "" {
				newName = *f.Filename
			}
			if newName != name {
				delete(files, name)
			}
			if _, clash := files[newName]; clash && newName != name {
				apiError(w, http.StatusUnprocessableEntity, "There is already a file called "+newName)
				return
			}
			files[newName] = content
			order = append(order, newName)
		}
	}
	var list []store.File
	seen := map[string]bool{}
	for _, name := range order {
		if c, ok := files[name]; ok && !seen[name] {
			list = append(list, store.File{Name: name, Content: c})
			seen[name] = true
		}
	}
	desc, list, err = store.Normalize(desc, list, s.cfg.Limits)
	if err != nil {
		s.apiStoreError(w, err)
		return
	}
	if m, err = s.store.Update(m.ID, desc, list); err != nil {
		s.apiStoreError(w, err)
		return
	}
	if in.Public != nil {
		if m, err = s.store.SetPublic(m.ID, *in.Public); err != nil {
			s.apiStoreError(w, err)
			return
		}
	}
	if rev, err = s.store.Revision(m.ID, m.Latest()); err != nil {
		s.apiStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.apiOut(r, m, rev))
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request) {
	if !s.apiAllow(w, r) {
		return
	}
	m, ok := s.apiOwner(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(m.ID); err != nil {
		s.apiStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
