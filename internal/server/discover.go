package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uug-ai/gist/internal/render"
)

const (
	discoverPageSize     = 20
	discoverPreviewLines = 10
)

type discoverData struct {
	Items    []discoverItem
	Total    int
	Page     int
	PrevPage int
	NextPage int
}

type discoverItem struct {
	ID        string
	Title     string
	CreatedAt time.Time
	Files     int
	FileName  string
	Language  string
	Preview   template.HTML
	Markdown  bool
	More      bool
}

// discover lists public gists, newest first, like GitHub's Discover.
func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	ids, total := s.store.PublicIDs((page-1)*discoverPageSize, discoverPageSize)
	d := &discoverData{Total: total, Page: page}
	if page > 1 {
		d.PrevPage = page - 1
	}
	if page*discoverPageSize < total {
		d.NextPage = page + 1
	}
	for _, id := range ids {
		m, err := s.store.Get(id)
		if err != nil {
			continue // deleted or expired since the index was read
		}
		rev, err := s.store.Revision(id, m.Latest())
		if err != nil || len(rev.Files) == 0 {
			continue
		}
		f := rev.Files[0]
		item := discoverItem{
			ID:        id,
			Title:     rev.Description,
			CreatedAt: m.CreatedAt,
			Files:     len(rev.Files),
			FileName:  f.Name,
			Language:  render.Language(f.Name, f.Content),
		}
		if item.Title == "" {
			item.Title = f.Name
		}
		preview, more := firstLines(f.Content, discoverPreviewLines)
		item.More = more
		if render.IsMarkdown(f.Name) {
			item.Markdown, item.Preview = true, render.Markdown(preview)
		} else {
			item.Preview = render.Code(f.Name, preview, "d"+id[:8]+"-L")
		}
		d.Items = append(d.Items, item)
	}
	if page == 1 {
		w.Header().Del("X-Robots-Tag")
	}
	s.render(w, r, "discover", pageData{Title: "Discover gists", Discover: d, Indexable: page == 1})
}

// firstLines returns the first n lines of s and whether there were more.
func firstLines(s string, n int) (string, bool) {
	i := 0
	for line := 0; line < n; line++ {
		j := strings.IndexByte(s[i:], '\n')
		if j < 0 {
			return s, false
		}
		i += j + 1
	}
	return s[:i], i < len(s)
}
