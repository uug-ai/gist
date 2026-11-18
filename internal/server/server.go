// Package server serves gist.uug.ai: the web pages, raw files, archives and
// the JSON API.
package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/uug-ai/gist/internal/render"
	"github.com/uug-ai/gist/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Config configures the server.
type Config struct {
	// PublicURL is the site's address, e.g. https://gist.uug.ai. Empty
	// derives it from each request.
	PublicURL string
	Limits    store.Limits
	// RateLimit is how many writes (create, edit, fork, delete) one client
	// may make per minute. Zero disables the limit.
	RateLimit int
	// TrustProxy reads the client address from X-Real-IP / X-Forwarded-For.
	TrustProxy bool
	Version    string
	Logger     *slog.Logger
}

// Server handles HTTP requests.
type Server struct {
	cfg       Config
	store     *store.Store
	pages     map[string]*template.Template
	static    fs.FS
	assetHash string
	chromaCSS []byte
	limiter   *limiter
	log       *slog.Logger
	handler   http.Handler
}

// New builds the server.
func New(cfg Config, st *store.Store) (*Server, error) {
	if cfg.Limits == (store.Limits{}) {
		cfg.Limits = store.DefaultLimits
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:       cfg,
		store:     st,
		static:    static,
		chromaCSS: []byte(render.CSS()),
		log:       cfg.Logger,
	}
	if cfg.RateLimit > 0 {
		s.limiter = newLimiter(cfg.RateLimit, time.Minute)
	}
	if s.assetHash, err = hashAssets(static, s.chromaCSS); err != nil {
		return nil, err
	}
	if s.pages, err = parseTemplates(); err != nil {
		return nil, err
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /favicon.ico", s.favicon)
	mux.HandleFunc("GET /-/assets/{path...}", s.asset)

	mux.HandleFunc("GET /{$}", s.newGist)
	mux.HandleFunc("POST /{$}", s.create)
	mux.HandleFunc("PUT /{$}", s.put)
	mux.HandleFunc("PUT /{name}", s.put)

	mux.HandleFunc("GET /{id}", s.view)
	mux.HandleFunc("GET /{id}/raw/{file}", s.raw)
	mux.HandleFunc("GET /{id}/revisions", s.revisions)
	mux.HandleFunc("GET /{id}/revisions/{rev}", s.view)
	mux.HandleFunc("GET /{id}/revisions/{rev}/raw/{file}", s.raw)
	mux.HandleFunc("GET /{id}/archive.zip", s.archive)
	mux.HandleFunc("GET /{id}/revisions/{rev}/archive.zip", s.archive)
	mux.HandleFunc("GET /{id}/edit", s.editForm)
	mux.HandleFunc("POST /{id}/edit", s.edit)
	mux.HandleFunc("POST /{id}/delete", s.delete)
	mux.HandleFunc("POST /{id}/fork", s.fork)
	mux.HandleFunc("GET /{id}/claim", s.claimForm)
	mux.HandleFunc("POST /{id}/claim", s.claim)

	mux.HandleFunc("POST /api/v1/gists", s.apiCreate)
	mux.HandleFunc("GET /api/v1/gists/{id}", s.apiGet)
	mux.HandleFunc("PATCH /api/v1/gists/{id}", s.apiUpdate)
	mux.HandleFunc("DELETE /api/v1/gists/{id}", s.apiDelete)

	// Browsers may not post forms here from other sites. Requests without
	// Origin/Sec-Fetch-Site headers (curl, scripts) are not affected.
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusForbidden, "Cross-site requests are not allowed.")
	}))
	return s.logRequests(securityHeaders(cop.Handler(mux)))
}

// securityHeaders sets headers every response gets. Pages are never indexed:
// gists are unlisted.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data: https:; font-src 'self'; connect-src 'self'; form-action 'self'; "+
			"base-uri 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logRequests logs the route pattern, never the path: gist ids are secret.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" {
			return
		}
		s.log.Info("request", "method", r.Method, "route", r.Pattern, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nAllow: /$\nDisallow: /\n")
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFileFS(w, r, s.static, "favicon.ico")
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	if r.URL.Query().Get("v") == s.assetHash {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	if name == "chroma.css" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(s.chromaCSS)
		return
	}
	if _, err := fs.Stat(s.static, name); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, s.static, name)
}

func hashAssets(static fs.FS, extra []byte) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(data)
		return nil
	})
	h.Write(extra)
	return hex.EncodeToString(h.Sum(nil))[:12], err
}

// baseURL is the public address of the site, without a trailing slash.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) secureCookies(r *http.Request) bool {
	return strings.HasPrefix(s.baseURL(r), "https://")
}

// wantsHTML reports whether the client is a browser rather than curl or a
// script, which get plain text.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// fail answers with an error page, or plain text for non-browsers.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if !wantsHTML(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintln(w, msg)
		return
	}
	s.renderStatus(w, r, status, "error", pageData{
		Title:   http.StatusText(status),
		Message: msg,
		Status:  status,
	})
}

// storeError maps store errors to responses.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "This gist doesn't exist, has expired or was deleted.")
	case errors.Is(err, store.ErrQuota):
		s.fail(w, r, http.StatusInsufficientStorage, "gist.uug.ai is out of space right now. Please try again later.")
	case errors.Is(err, store.ErrTooManyRevisions):
		s.fail(w, r, http.StatusConflict, "This gist has reached its maximum number of revisions. Fork it to keep editing.")
	case store.IsValidation(err):
		s.fail(w, r, http.StatusBadRequest, err.Error())
	default:
		s.log.Error("store", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
}

// allow applies the write rate limit, answering 429 when it's exceeded.
func (s *Server) allow(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter == nil || s.limiter.allow(s.clientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	s.fail(w, r, http.StatusTooManyRequests, "Too many changes in a short time. Please wait a minute.")
	return false
}

// Owner cookies hold a gist's edit key, scoped to that gist's path.
func cookieName(id string) string { return "gist_" + id }

func (s *Server) setOwner(w http.ResponseWriter, r *http.Request, id, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(id),
		Value:    token,
		Path:     "/" + id,
		MaxAge:   400 * 24 * 3600,
		HttpOnly: true,
		Secure:   s.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearOwner(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName(id), Value: "", Path: "/" + id, MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode,
	})
}

// ownerToken is the edit key the request carries: the owner cookie, or an
// Authorization header for API and terminal clients.
func ownerToken(r *http.Request, id string) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		for _, prefix := range []string{"Bearer ", "bearer ", "token "} {
			if strings.HasPrefix(auth, prefix) {
				return strings.TrimSpace(auth[len(prefix):])
			}
		}
	}
	if c, err := r.Cookie(cookieName(id)); err == nil {
		return c.Value
	}
	return ""
}

func gistPath(id string) string { return "/" + id }

func rawPath(id string, rev int, latest bool, name string) string {
	if latest {
		return "/" + id + "/raw/" + url.PathEscape(name)
	}
	return fmt.Sprintf("/%s/revisions/%d/raw/%s", id, rev, url.PathEscape(name))
}

var expiries = map[string]time.Duration{
	"":    0,
	"1h":  time.Hour,
	"1d":  24 * time.Hour,
	"1w":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func parseExpiry(v string) (time.Duration, error) {
	d, ok := expiries[strings.TrimSpace(v)]
	if !ok {
		return 0, fmt.Errorf("expires must be one of 1h, 1d, 1w or 30d")
	}
	return d, nil
}
