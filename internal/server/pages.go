package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/uug-ai/gist/internal/diff"
	"github.com/uug-ai/gist/internal/render"
	"github.com/uug-ai/gist/internal/store"
)

type pageData struct {
	Title   string
	Message string
	Status  int
	Base    string
	Assets  string
	Version string
	Page    string
	// Indexable pages may appear in search results: the home page and the
	// terminal page. Gists never do: every gist is secret.
	Indexable bool

	Form  *formData
	Gist  *gistView
	Revs  []revView
	Claim *claimData
}

type formData struct {
	Editing     bool
	ID          string
	Description string
	Expires     string
	Files       []store.File
	Error       string
	MaxFiles    int
	MaxSize     string
}

type gistView struct {
	ID          string
	URL         string
	Description string
	Title       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   *time.Time
	ForkOf      string
	Rev         int
	Latest      int
	IsLatest    bool
	Files       []fileView
	Owner       bool
	Token       string
	Created     bool
	ArchiveURL  string
}

type fileView struct {
	Name     string
	Anchor   string
	Language string
	Size     string
	Lines    int
	HTML     template.HTML
	Markdown bool
	RawURL   string
}

type revView struct {
	Number      int
	CreatedAt   time.Time
	Description string
	DescChanged bool
	Files       []fileDiff
	Added       int
	Removed     int
}

type fileDiff struct {
	Name    string
	OldName string
	Status  string
	Added   int
	Removed int
	Hunks   []diff.Hunk
}

type claimData struct {
	ID    string
	Error string
}

var funcs = template.FuncMap{
	"ago": func(t time.Time) string { return ago(time.Since(t)) },
	"until": func(t time.Time) string {
		d := time.Until(t)
		if d <= 0 {
			return "now"
		}
		return "in " + strings.TrimSuffix(ago(d), " ago")
	},
	"iso":   func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"short": func(id string) string { return id[:8] },
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	},
	"lineNo": func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	},
	"opClass": func(op diff.Op) string {
		switch op {
		case diff.Insert:
			return "add"
		case diff.Delete:
			return "del"
		}
		return "ctx"
	},
	"opSign": func(op diff.Op) string {
		switch op {
		case diff.Insert:
			return "+"
		case diff.Delete:
			return "-"
		}
		return " "
	},
	"slug": render.Slug,
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	case d < 365*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month") + " ago"
	}
	return plural(int(d/(365*24*time.Hour)), "year") + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func parseTemplates() (map[string]*template.Template, error) {
	pages := map[string]*template.Template{}
	for _, name := range []string{"new", "gist", "revisions", "claim", "error", "terminal"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return pages, nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, d pageData) {
	s.renderStatus(w, r, http.StatusOK, name, d)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, d pageData) {
	d.Base = s.baseURL(r)
	d.Assets = s.assetHash
	d.Version = s.cfg.Version
	d.Page = name
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", d); err != nil {
		s.log.Error("template", "page", name, "error", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) emptyForm() *formData {
	return &formData{
		Files:    []store.File{{}},
		MaxFiles: s.cfg.Limits.MaxFiles,
		MaxSize:  store.HumanBytes(int64(s.cfg.Limits.MaxBytes)),
	}
}

func (s *Server) newGist(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("X-Robots-Tag")
	s.render(w, r, "new", pageData{Title: "New secret gist", Form: s.emptyForm(), Indexable: true})
}

// terminal explains uploading from a terminal and the JSON API.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("X-Robots-Tag")
	s.render(w, r, "terminal", pageData{Title: "Terminal and API", Form: s.emptyForm(), Indexable: true})
}

// readForm reads the web form: description, expires and parallel filename
// and content fields.
func (s *Server) readForm(w http.ResponseWriter, r *http.Request) (*formData, error) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.Limits.MaxBytes)*3+64<<10)
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("The gist is too large (at most %s).", store.HumanBytes(int64(s.cfg.Limits.MaxBytes)))
	}
	f := s.emptyForm()
	f.Description = r.PostForm.Get("description")
	f.Expires = r.PostForm.Get("expires")
	names, contents := r.PostForm["filename"], r.PostForm["content"]
	f.Files = nil
	for i, c := range contents {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		f.Files = append(f.Files, store.File{Name: name, Content: c})
	}
	if len(f.Files) == 0 {
		f.Files = []store.File{{}}
	}
	return f, nil
}

// errPublic answers clients that ask for a public gist (public=true, as
// GitHub's API allows): gist.uug.ai never lists or indexes a gist.
const errPublic = "Public gists are not supported: every gist on gist.uug.ai is secret, only visible to people with its link."

// asksPublic reports whether a client asked for a public gist.
func asksPublic(v url.Values) bool {
	switch strings.ToLower(strings.TrimSpace(v.Get("public"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// upload is a gist sent by a terminal client (or a fork).
type upload struct {
	description string
	expires     string
	files       []store.File
	forkOf      string
}

// readUploads reads files sent with curl -F file=@name (multipart).
func (s *Server) readUploads(w http.ResponseWriter, r *http.Request) (upload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.Limits.MaxBytes)+64<<10)
	if err := r.ParseMultipartForm(int64(s.cfg.Limits.MaxBytes) + 64<<10); err != nil {
		return upload{}, fmt.Errorf("The upload is too large or malformed (at most %s).", store.HumanBytes(int64(s.cfg.Limits.MaxBytes)))
	}
	defer r.MultipartForm.RemoveAll()
	var files []store.File
	for _, field := range []string{"file", "files", "f"} {
		for _, fh := range r.MultipartForm.File[field] {
			fp, err := fh.Open()
			if err != nil {
				return upload{}, err
			}
			data, err := io.ReadAll(fp)
			fp.Close()
			if err != nil {
				return upload{}, err
			}
			files = append(files, store.File{Name: fh.Filename, Content: string(data)})
		}
	}
	// curl -F content=… -F filename=… works too.
	if c := r.FormValue("content"); c != "" {
		files = append(files, store.File{Name: r.FormValue("filename"), Content: c})
	}
	return upload{
		description: r.FormValue("description"),
		expires:     r.FormValue("expires"),
		files:       files,
	}, nil
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		u, err := s.readUploads(w, r)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		if asksPublic(r.MultipartForm.Value) {
			s.fail(w, r, http.StatusBadRequest, errPublic)
			return
		}
		s.createAndRespond(w, r, u)
		return
	}
	f, err := s.readForm(w, r)
	if err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if asksPublic(r.PostForm) {
		s.fail(w, r, http.StatusBadRequest, errPublic)
		return
	}
	if !wantsHTML(r) {
		s.createAndRespond(w, r, upload{description: f.Description, expires: f.Expires, files: f.Files})
		return
	}
	expiresIn, err := parseExpiry(f.Expires)
	if err != nil {
		f.Error = err.Error()
		s.renderStatus(w, r, http.StatusBadRequest, "new", pageData{Title: "New gist", Form: f})
		return
	}
	desc, files, err := store.Normalize(f.Description, f.Files, s.cfg.Limits)
	if err != nil {
		if store.IsValidation(err) {
			f.Error = err.Error()
			s.renderStatus(w, r, http.StatusBadRequest, "new", pageData{Title: "New gist", Form: f})
			return
		}
		s.storeError(w, r, err)
		return
	}
	m, token, err := s.store.Create(store.CreateInput{Description: desc, Files: files, ExpiresIn: expiresIn})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.setOwner(w, r, m.ID, token)
	http.Redirect(w, r, gistPath(m.ID)+"?created=1", http.StatusSeeOther)
}

// createAndRespond creates a gist for a terminal client and answers with its
// URL; the edit key is in the Gist-Edit-Key header.
func (s *Server) createAndRespond(w http.ResponseWriter, r *http.Request, u upload) {
	expiresIn, err := parseExpiry(u.expires)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	desc, files, err := store.Normalize(u.description, u.files, s.cfg.Limits)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	m, token, err := s.store.Create(store.CreateInput{
		Description: desc, Files: files, ExpiresIn: expiresIn, ForkOf: u.forkOf,
	})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	link := s.baseURL(r) + gistPath(m.ID)
	if wantsHTML(r) {
		s.setOwner(w, r, m.ID, token)
		http.Redirect(w, r, gistPath(m.ID)+"?created=1", http.StatusSeeOther)
		return
	}
	w.Header().Set("Location", link)
	w.Header().Set("Gist-Edit-Key", token)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, link)
}

// put handles curl -T file https://gist.uug.ai/ (PUT /file).
func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "-" {
		name = ""
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.Limits.MaxBytes))
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("The file is too large (at most %s).", store.HumanBytes(int64(s.cfg.Limits.MaxBytes))))
		return
	}
	q := r.URL.Query()
	if asksPublic(q) {
		s.fail(w, r, http.StatusBadRequest, errPublic)
		return
	}
	if n := q.Get("filename"); n != "" {
		name = n
	}
	s.createAndRespond(w, r, upload{
		description: q.Get("description"),
		expires:     q.Get("expires"),
		files:       []store.File{{Name: name, Content: string(data)}},
	})
}

// load reads a gist and the requested revision (the latest when the route
// has no {rev}).
func (s *Server) load(w http.ResponseWriter, r *http.Request) (*store.Meta, *store.Revision, bool) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		s.storeError(w, r, err)
		return nil, nil, false
	}
	n := m.Latest()
	if v := r.PathValue("rev"); v != "" {
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > m.Latest() {
			s.fail(w, r, http.StatusNotFound, "This revision doesn't exist.")
			return nil, nil, false
		}
	}
	rev, err := s.store.Revision(id, n)
	if err != nil {
		s.storeError(w, r, err)
		return nil, nil, false
	}
	return m, rev, true
}

func (s *Server) view(w http.ResponseWriter, r *http.Request) {
	m, rev, ok := s.load(w, r)
	if !ok {
		return
	}
	if !wantsHTML(r) && len(rev.Files) == 1 && r.PathValue("rev") == "" {
		// curl https://gist.uug.ai/<id> prints the file.
		s.writeRaw(w, rev.Files[0])
		return
	}
	g := s.gistView(r, m, rev)
	s.render(w, r, "gist", pageData{Title: g.Title, Gist: g})
}

func (s *Server) gistView(r *http.Request, m *store.Meta, rev *store.Revision) *gistView {
	latest := rev.Number == m.Latest()
	g := &gistView{
		ID:          m.ID,
		URL:         s.baseURL(r) + gistPath(m.ID),
		Description: rev.Description,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		ExpiresAt:   m.ExpiresAt,
		ForkOf:      m.ForkOf,
		Rev:         rev.Number,
		Latest:      m.Latest(),
		IsLatest:    latest,
		Created:     r.URL.Query().Get("created") == "1",
		ArchiveURL:  gistPath(m.ID) + "/archive.zip",
	}
	if !latest {
		g.ArchiveURL = fmt.Sprintf("/%s/revisions/%d/archive.zip", m.ID, rev.Number)
	}
	if token := ownerToken(r, m.ID); store.Authorized(m, token) {
		g.Owner, g.Token = true, token
	} else if s.isAdmin(token) {
		g.Owner = true // moderation: edit and delete, but no edit key to show
	}
	for _, f := range rev.Files {
		anchor := "file-" + render.Slug(f.Name)
		fv := fileView{
			Name:     f.Name,
			Anchor:   anchor,
			Language: render.Language(f.Name, f.Content),
			Size:     store.HumanBytes(int64(len(f.Content))),
			Lines:    len(diff.SplitLines(f.Content)),
			RawURL:   rawPath(m.ID, rev.Number, latest, f.Name),
		}
		if render.IsMarkdown(f.Name) {
			fv.Markdown, fv.HTML = true, render.Markdown(f.Content)
		} else {
			fv.HTML = render.Code(f.Name, f.Content, anchor+"-L")
		}
		g.Files = append(g.Files, fv)
	}
	g.Title = g.Description
	if g.Title == "" && len(rev.Files) > 0 {
		g.Title = rev.Files[0].Name
	}
	return g
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	_, rev, ok := s.load(w, r)
	if !ok {
		return
	}
	name := r.PathValue("file")
	for _, f := range rev.Files {
		if f.Name == name {
			s.writeRaw(w, f)
			return
		}
	}
	s.fail(w, r, http.StatusNotFound, "This gist has no file called "+strconv.Quote(name)+".")
}

func (s *Server) writeRaw(w http.ResponseWriter, f store.File) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.Name}))
	_, _ = io.WriteString(w, f.Content)
}

func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	m, rev, ok := s.load(w, r)
	if !ok {
		return
	}
	folder := fmt.Sprintf("gist-%s-r%d", m.ID[:8], rev.Number)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range rev.Files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: folder + "/" + f.Name, Method: zip.Deflate, Modified: rev.CreatedAt})
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		if _, err := io.WriteString(fw, f.Content); err != nil {
			s.storeError(w, r, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": folder + ".zip"}))
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) revisions(w http.ResponseWriter, r *http.Request) {
	m, latest, ok := s.load(w, r)
	if !ok {
		return
	}
	g := s.gistView(r, m, latest)
	g.Files = nil // the header only
	var revs []revView
	var prev *store.Revision
	for n := 1; n <= m.Latest(); n++ {
		rev, err := s.store.Revision(m.ID, n)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		revs = append(revs, compare(prev, rev))
		prev = rev
	}
	for i, j := 0, len(revs)-1; i < j; i, j = i+1, j-1 {
		revs[i], revs[j] = revs[j], revs[i]
	}
	s.render(w, r, "revisions", pageData{Title: "Revisions · " + g.Title, Gist: g, Revs: revs})
}

// compare describes what changed from prev (nil for the first revision) to
// rev. A removed and an added file with the same content are a rename.
func compare(prev, rev *store.Revision) revView {
	v := revView{Number: rev.Number, CreatedAt: rev.CreatedAt, Description: rev.Description}
	old := map[string]string{}
	if prev != nil {
		for _, f := range prev.Files {
			old[f.Name] = f.Content
		}
		v.DescChanged = prev.Description != rev.Description
	}
	cur := map[string]bool{}
	for _, f := range rev.Files {
		cur[f.Name] = true
	}
	renamedFrom := map[string]string{}
	if prev != nil {
		for _, f := range rev.Files {
			if _, ok := old[f.Name]; ok {
				continue
			}
			for _, p := range prev.Files {
				if !cur[p.Name] && p.Content == f.Content && !isValue(renamedFrom, p.Name) {
					renamedFrom[f.Name] = p.Name
					break
				}
			}
		}
	}
	for _, f := range rev.Files {
		fd := fileDiff{Name: f.Name}
		before, existed := old[f.Name]
		switch {
		case renamedFrom[f.Name] != "":
			fd.Status, fd.OldName = "renamed", renamedFrom[f.Name]
		case !existed:
			fd.Status = "added"
			fd.Hunks, fd.Added, fd.Removed = hunks("", f.Content)
		case before != f.Content:
			fd.Status = "modified"
			fd.Hunks, fd.Added, fd.Removed = hunks(before, f.Content)
		default:
			continue
		}
		v.Files = append(v.Files, fd)
	}
	if prev != nil {
		for _, p := range prev.Files {
			if cur[p.Name] || isValue(renamedFrom, p.Name) {
				continue
			}
			fd := fileDiff{Name: p.Name, Status: "deleted"}
			fd.Hunks, fd.Added, fd.Removed = hunks(p.Content, "")
			v.Files = append(v.Files, fd)
		}
	}
	for _, f := range v.Files {
		v.Added += f.Added
		v.Removed += f.Removed
	}
	return v
}

func isValue(m map[string]string, v string) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

func hunks(a, b string) ([]diff.Hunk, int, int) {
	lines := diff.Lines(diff.SplitLines(a), diff.SplitLines(b))
	added, removed := diff.Stat(lines)
	return diff.Hunks(lines, 3), added, removed
}

// requireOwner loads the gist and checks the edit key. Browsers without the
// key are sent to the page where they can enter it.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) (*store.Meta, bool) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return nil, false
	}
	if !s.authorized(m, ownerToken(r, m.ID)) {
		if wantsHTML(r) && r.Method == http.MethodGet {
			http.Redirect(w, r, gistPath(m.ID)+"/claim", http.StatusSeeOther)
		} else {
			s.fail(w, r, http.StatusForbidden, "You need this gist's edit key to change it.")
		}
		return nil, false
	}
	return m, true
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request) {
	m, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	rev, err := s.store.Revision(m.ID, m.Latest())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	f := s.emptyForm()
	f.Editing, f.ID, f.Description, f.Files = true, m.ID, rev.Description, rev.Files
	s.render(w, r, "new", pageData{Title: "Edit gist", Form: f})
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	m, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	f, err := s.readForm(w, r)
	if err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	f.Editing, f.ID = true, m.ID
	desc, files, err := store.Normalize(f.Description, f.Files, s.cfg.Limits)
	if err == nil {
		_, err = s.store.Update(m.ID, desc, files)
	}
	if err != nil {
		if store.IsValidation(err) && wantsHTML(r) {
			f.Error = err.Error()
			s.renderStatus(w, r, http.StatusBadRequest, "new", pageData{Title: "Edit gist", Form: f})
			return
		}
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, gistPath(m.ID), http.StatusSeeOther)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	m, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(m.ID); err != nil {
		s.storeError(w, r, err)
		return
	}
	s.clearOwner(w, r, m.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	m, rev, ok := s.load(w, r)
	if !ok {
		return
	}
	s.createAndRespond(w, r, upload{description: rev.Description, files: rev.Files, forkOf: m.ID})
}

func (s *Server) claimForm(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, "claim", pageData{Title: "Enter edit key", Claim: &claimData{ID: m.ID}})
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r) {
		return
	}
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	key := strings.TrimSpace(r.PostFormValue("key"))
	if !s.authorized(m, key) {
		s.renderStatus(w, r, http.StatusForbidden, "claim", pageData{
			Title: "Enter edit key",
			Claim: &claimData{ID: m.ID, Error: "That key doesn't match this gist."},
		})
		return
	}
	s.setOwner(w, r, m.ID, key)
	http.Redirect(w, r, gistPath(m.ID), http.StatusSeeOther)
}
