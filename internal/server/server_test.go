package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/uug-ai/gist/internal/store"
)

func newTestServer(t *testing.T, cfg Config) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.NewMemory(store.Options{})
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, st
}

// browser is a client that keeps cookies, doesn't follow redirects and
// asks for HTML.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar := newJar()
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type jar struct{ cookies map[string][]*http.Cookie }

func newJar() *jar { return &jar{cookies: map[string][]*http.Cookie{}} }

func (j *jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	for _, c := range cookies {
		list := j.cookies[c.Path]
		kept := list[:0]
		for _, old := range list {
			if old.Name != c.Name {
				kept = append(kept, old)
			}
		}
		if c.MaxAge >= 0 {
			kept = append(kept, c)
		}
		j.cookies[c.Path] = kept
	}
}

func (j *jar) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	for path, list := range j.cookies {
		if u.Path == path || strings.HasPrefix(u.Path, path+"/") {
			out = append(out, list...)
		}
	}
	return out
}

func do(t *testing.T, c *http.Client, method, u, contentType string, body io.Reader, html bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if html {
		req.Header.Set("Accept", "text/html")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func form(values url.Values) io.Reader { return strings.NewReader(values.Encode()) }

const formType = "application/x-www-form-urlencoded"

func TestWebFlow(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	c := browser(t)

	resp, body := do(t, c, "GET", ts.URL+"/", "", nil, true)
	if resp.StatusCode != 200 || !strings.Contains(body, "Create gist") {
		t.Fatalf("home: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP %q", csp)
	}

	resp, _ = do(t, c, "POST", ts.URL+"/", formType, form(url.Values{
		"description": {"Hello <world>"},
		"filename":    {"hello.py", "notes.md", ""},
		"content":     {"print('hi')\n", "# Title\n\n<script>alert(1)</script>\n[x](javascript:alert(1))\n", ""},
		"expires":     {""},
	}), true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	id := strings.TrimSuffix(strings.TrimPrefix(loc, "/"), "?created=1")
	if !store.ValidID(id) {
		t.Fatalf("location %q", loc)
	}

	resp, body = do(t, c, "GET", ts.URL+loc, "", nil, true)
	if resp.StatusCode != 200 {
		t.Fatalf("view: %d", resp.StatusCode)
	}
	for _, want := range []string{"Hello &lt;world&gt;", "Your gist is ready", `class="chroma"`, "<h1>Title</h1>", "Edit key", "/" + id + "/edit"} {
		if !strings.Contains(body, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	for _, bad := range []string{"<script>alert", `href="javascript:`} {
		if strings.Contains(body, bad) {
			t.Errorf("view contains %q", bad)
		}
	}

	// Someone else sees no edit controls and can't edit.
	other := browser(t)
	_, body = do(t, other, "GET", ts.URL+"/"+id, "", nil, true)
	if strings.Contains(body, "/"+id+"/edit\"") || strings.Contains(body, "Edit key</summary>") {
		t.Error("non-owner sees owner controls")
	}
	resp, _ = do(t, other, "GET", ts.URL+"/"+id+"/edit", "", nil, true)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(resp.Header.Get("Location"), "/claim") {
		t.Errorf("non-owner edit: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, other, "POST", ts.URL+"/"+id+"/delete", formType, nil, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner delete: %d", resp.StatusCode)
	}

	// Edit: rename notes.md, change hello.py.
	resp, _ = do(t, c, "POST", ts.URL+"/"+id+"/edit", formType, form(url.Values{
		"description": {"Hello"},
		"filename":    {"hello.py", "readme.md"},
		"content":     {"print('hello')\n", "# Title\n\n<script>alert(1)</script>\n[x](javascript:alert(1))\n"},
	}), true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	_, body = do(t, c, "GET", ts.URL+"/"+id+"/revisions", "", nil, true)
	for _, want := range []string{"Revision 2", "notes.md → readme.md", "renamed", "modified", "print(&#39;hello&#39;)"} {
		if !strings.Contains(body, want) {
			t.Errorf("revisions lack %q", want)
		}
	}

	// Old revision and its raw file.
	resp, body = do(t, c, "GET", ts.URL+"/"+id+"/revisions/1/raw/hello.py", "", nil, false)
	if resp.StatusCode != 200 || body != "print('hi')\n" {
		t.Errorf("raw r1: %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("raw content type %q", ct)
	}
	resp, body = do(t, c, "GET", ts.URL+"/"+id+"/revisions/1", "", nil, true)
	if resp.StatusCode != 200 || !strings.Contains(body, "revision 1 of 2") {
		t.Errorf("view r1: %d", resp.StatusCode)
	}

	// ZIP.
	resp, body = do(t, c, "GET", ts.URL+"/"+id+"/archive.zip", "", nil, false)
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("zip: %v", err)
	}

	// Fork by someone else.
	resp, _ = do(t, other, "POST", ts.URL+"/"+id+"/fork", formType, nil, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("fork: %d", resp.StatusCode)
	}
	forkLoc := resp.Header.Get("Location")
	_, body = do(t, other, "GET", ts.URL+forkLoc, "", nil, true)
	if !strings.Contains(body, "forked from") || !strings.Contains(body, "Edit key") {
		t.Error("fork page lacks fork link or owner controls")
	}

	// Delete.
	resp, _ = do(t, c, "POST", ts.URL+"/"+id+"/delete", formType, nil, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "GET", ts.URL+"/"+id, "", nil, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete: %d", resp.StatusCode)
	}
}

func TestFormErrorsKeepInput(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	resp, body := do(t, browser(t), "POST", ts.URL+"/", formType, form(url.Values{
		"description": {"keep me"},
		"filename":    {"a/b.txt"},
		"content":     {"some content"},
	}), true)
	if resp.StatusCode != 400 || !strings.Contains(body, "keep me") || !strings.Contains(body, "some content") || !strings.Contains(body, "slashes") {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestClaim(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	req, _ := http.NewRequest("PUT", ts.URL+"/x.txt", strings.NewReader("hello"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	key := resp.Header.Get("Gist-Edit-Key")
	id := strings.TrimPrefix(resp.Header.Get("Location"), ts.URL+"/")

	c := browser(t)
	resp, _ = do(t, c, "POST", ts.URL+"/"+id+"/claim", formType, form(url.Values{"key": {"wrong"}}), true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "POST", ts.URL+"/"+id+"/claim", formType, form(url.Values{"key": {key}}), true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("right key: %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "GET", ts.URL+"/"+id+"/edit", "", nil, true)
	if resp.StatusCode != 200 {
		t.Fatalf("edit after claim: %d", resp.StatusCode)
	}
}

func TestTerminal(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	c := http.DefaultClient

	// curl -T main.go
	resp, body := do(t, c, "PUT", ts.URL+"/main.go?description=from+curl&expires=1d", "", strings.NewReader("package main\n"), false)
	if resp.StatusCode != http.StatusCreated || !strings.HasPrefix(body, ts.URL+"/") {
		t.Fatalf("put: %d %q", resp.StatusCode, body)
	}
	u := strings.TrimSpace(body)
	_, body = do(t, c, "GET", u, "", nil, false)
	if body != "package main\n" {
		t.Errorf("curl view of a single-file gist: %q", body)
	}

	// curl -T - (stdin)
	resp, body = do(t, c, "PUT", ts.URL+"/", "", strings.NewReader("log line\n"), false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put stdin: %d", resp.StatusCode)
	}
	_, body = do(t, c, "GET", strings.TrimSpace(body)+"/raw/gistfile1.txt", "", nil, false)
	if body != "log line\n" {
		t.Errorf("stdin upload: %q", body)
	}

	// curl -F file=@a -F file=@b
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range map[string]string{"a.go": "package a\n", "b.go": "package b\n"} {
		w, _ := mw.CreateFormFile("file", name)
		io.WriteString(w, content)
	}
	mw.WriteField("description", "two files")
	mw.Close()
	resp, body = do(t, c, "POST", ts.URL+"/", mw.FormDataContentType(), &buf, false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("multipart: %d %s", resp.StatusCode, body)
	}
	_, body = do(t, c, "GET", strings.TrimSpace(body)+"/raw/b.go", "", nil, false)
	if body != "package b\n" {
		t.Errorf("multipart file: %q", body)
	}

	// Too large.
	resp, _ = do(t, c, "PUT", ts.URL+"/big.txt", "", strings.NewReader(strings.Repeat("x", store.DefaultLimits.MaxBytes+1)), false)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", resp.StatusCode)
	}
}

func TestAPI(t *testing.T) {
	ts, _ := newTestServer(t, Config{PublicURL: "https://gist.example"})
	c := http.DefaultClient
	resp, body := do(t, c, "POST", ts.URL+"/api/v1/gists", "application/json",
		strings.NewReader(`{"description":"api","files":{"a.txt":{"content":"A"},"b.txt":{"content":"B"}}}`), false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var out apiGistOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.EditKey == "" || out.HTMLURL != "https://gist.example/"+out.ID || len(out.Files) != 2 {
		t.Fatalf("create output %+v", out)
	}

	patch := func(key, body string) (*http.Response, string) {
		req, _ := http.NewRequest("PATCH", ts.URL+"/api/v1/gists/"+out.ID, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}
	if resp, _ := patch("", `{"description":"x"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("patch without key: %d", resp.StatusCode)
	}
	resp, body = patch(out.EditKey, `{"files":{"a.txt":null,"b.txt":{"filename":"c.txt"},"d.txt":{"content":"D"}}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	var patched apiGistOut
	json.Unmarshal([]byte(body), &patched)
	if patched.Revision != 2 || len(patched.Files) != 2 || patched.Files["c.txt"].Content != "B" || patched.Files["d.txt"].Content != "D" {
		t.Fatalf("patched %+v", patched)
	}
	if resp, _ := patch(out.EditKey, `{"files":{"c.txt":null,"d.txt":null}}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("deleting every file: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/gists/"+out.ID, nil)
	req.Header.Set("Authorization", "Bearer "+out.EditKey)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %v %d", err, resp.StatusCode)
	}
	resp, _ = do(t, c, "GET", ts.URL+"/api/v1/gists/"+out.ID, "", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: %d", resp.StatusCode)
	}
}

func TestCrossSiteFormIsRejected(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	req, _ := http.NewRequest("POST", ts.URL+"/", form(url.Values{"filename": {"a"}, "content": {"b"}}))
	req.Header.Set("Content-Type", formType)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site post: %d", resp.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	ts, _ := newTestServer(t, Config{RateLimit: 2})
	for i, want := range []int{201, 201, 429} {
		resp, _ := do(t, http.DefaultClient, "PUT", ts.URL+"/a.txt", "", strings.NewReader("x"), false)
		if resp.StatusCode != want {
			t.Fatalf("request %d: %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestAssetsAndRobots(t *testing.T) {
	ts, _ := newTestServer(t, Config{})
	for path, want := range map[string]string{
		"/-/assets/style.css":  "--primary",
		"/-/assets/app.js":     "gist.uug.ai",
		"/-/assets/chroma.css": ".chroma",
		"/robots.txt":          "Disallow: /",
	} {
		resp, body := do(t, http.DefaultClient, "GET", ts.URL+path, "", nil, false)
		if resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	resp, _ := do(t, http.DefaultClient, "GET", ts.URL+"/-/assets/missing.css", "", nil, false)
	if resp.StatusCode != 404 {
		t.Errorf("missing asset: %d", resp.StatusCode)
	}
}
