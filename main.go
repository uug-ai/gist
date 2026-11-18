// Command gist serves gist.uug.ai: share code and text snippets in one click.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/uug-ai/gist/internal/server"
	"github.com/uug-ai/gist/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, env func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gist", flag.ContinueOnError)
	flags.SetOutput(stderr)
	selfTest := flags.Bool("self-test", false, "start in memory, check the main routes on loopback and exit")
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: gist [--self-test | --version]")
		fmt.Fprintln(stderr, "Configuration is read from the environment; see README.md.")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		flags.Usage()
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if *selfTest {
		if err := runSelfTest(logger); err != nil {
			fmt.Fprintln(stderr, "self-test failed:", err)
			return 1
		}
		fmt.Fprintln(stdout, "self-test ok")
		return 0
	}

	cfg, err := loadConfig(env)
	if err != nil {
		logger.Error("configuration", "error", err)
		return 1
	}
	st, err := store.Open(cfg.dataDir, store.Options{Quota: cfg.quota, MaxRevisions: cfg.maxRevisions})
	if err != nil {
		logger.Error("store", "error", err)
		return 1
	}
	cfg.server.Logger = logger
	srv, err := server.New(cfg.server, st)
	if err != nil {
		logger.Error("server", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go sweep(ctx, st, logger)

	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpServer.ListenAndServe() }()
	logger.Info("listening", "addr", cfg.addr, "version", version, "data", cfg.dataDir,
		"usage_bytes", st.Usage(), "public_url", cfg.server.PublicURL)

	select {
	case err := <-errc:
		logger.Error("listen", "error", err)
		return 1
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		logger.Error("shutdown", "error", err)
		return 1
	}
	logger.Info("stopped")
	return 0
}

type config struct {
	addr         string
	dataDir      string
	quota        int64
	maxRevisions int
	server       server.Config
}

func loadConfig(env func(string) string) (config, error) {
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(env(key)); v != "" {
			return v
		}
		return fallback
	}
	var errs []error
	integer := func(key string, fallback int64) int64 {
		v := get(key, "")
		if v == "" {
			return fallback
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s must be a non-negative integer", key))
			return fallback
		}
		return n
	}
	boolean := func(key string) bool {
		switch strings.ToLower(get(key, "")) {
		case "1", "true", "yes":
			return true
		}
		return false
	}

	limits := store.DefaultLimits
	limits.MaxBytes = int(integer("GIST_MAX_BYTES", int64(limits.MaxBytes)))
	limits.MaxFiles = int(integer("GIST_MAX_FILES", int64(limits.MaxFiles)))
	cfg := config{
		addr:         ":" + get("PORT", "8080"),
		dataDir:      get("GIST_DATA_DIR", "/data"),
		quota:        integer("GIST_STORE_QUOTA_BYTES", 4<<30),
		maxRevisions: int(integer("GIST_MAX_REVISIONS", 100)),
		server: server.Config{
			PublicURL:  get("GIST_PUBLIC_URL", ""),
			Limits:     limits,
			RateLimit:  int(integer("GIST_RATE_LIMIT", 30)),
			TrustProxy: boolean("GIST_TRUST_PROXY"),
			AdminToken: get("GIST_ADMIN_TOKEN", ""),
			Version:    version,
		},
	}
	if limits.MaxBytes < 1 || limits.MaxFiles < 1 {
		errs = append(errs, errors.New("GIST_MAX_BYTES and GIST_MAX_FILES must be at least 1"))
	}
	if t := cfg.server.AdminToken; t != "" && len(t) < 32 {
		errs = append(errs, errors.New("GIST_ADMIN_TOKEN must be at least 32 characters"))
	}
	if u := cfg.server.PublicURL; u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		errs = append(errs, errors.New("GIST_PUBLIC_URL must start with http:// or https://"))
	}
	return cfg, errors.Join(errs...)
}

// sweep deletes expired gists every few minutes.
func sweep(ctx context.Context, st *store.Store, logger *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if n, err := st.Sweep(); err != nil {
			logger.Error("sweep", "error", err)
		} else if n > 0 {
			logger.Info("sweep", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runSelfTest serves an in-memory store on loopback and checks that a gist
// can be created, viewed, read raw and edited. It needs no disk and no
// network beyond loopback, so it runs in the hardened image's smoke test.
func runSelfTest(logger *slog.Logger) error {
	srv, err := server.New(server.Config{Version: version, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		store.NewMemory(store.Options{}))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	go hs.Serve(ln)
	defer hs.Close()
	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 10 * time.Second}

	expect := func(req *http.Request, status int, contains string) (*http.Response, string, error) {
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", err
		}
		if resp.StatusCode != status {
			return nil, "", fmt.Errorf("%s %s: status %d, want %d", req.Method, req.URL.Path, resp.StatusCode, status)
		}
		if !strings.Contains(string(body), contains) {
			return nil, "", fmt.Errorf("%s %s: body doesn't contain %q", req.Method, req.URL.Path, contains)
		}
		return resp, string(body), nil
	}
	get := func(path string, accept string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		return req
	}

	if _, _, err := expect(get("/healthz", ""), http.StatusOK, "ok"); err != nil {
		return err
	}
	if _, _, err := expect(get("/", "text/html"), http.StatusOK, "Create secret gist"); err != nil {
		return err
	}
	const source = "package main\n\nfunc main() { println(\"self-test\") }\n"
	put, _ := http.NewRequest(http.MethodPut, base+"/main.go", strings.NewReader(source))
	resp, body, err := expect(put, http.StatusCreated, base+"/")
	if err != nil {
		return err
	}
	gistURL := strings.TrimSpace(body)
	id := strings.TrimPrefix(gistURL, base+"/")
	key := resp.Header.Get("Gist-Edit-Key")
	if !store.ValidID(id) || key == "" {
		return fmt.Errorf("unexpected create response %q", body)
	}
	if _, _, err := expect(get("/"+id, "text/html"), http.StatusOK, `class="chroma"`); err != nil {
		return err
	}
	if _, raw, err := expect(get("/"+id+"/raw/main.go", ""), http.StatusOK, "self-test"); err != nil {
		return err
	} else if raw != source {
		return errors.New("raw content differs from the upload")
	}
	patch, _ := http.NewRequest(http.MethodPatch, base+"/api/v1/gists/"+id,
		strings.NewReader(`{"files":{"README.md":{"content":"# Hello"}}}`))
	patch.Header.Set("Authorization", "Bearer "+key)
	if _, _, err := expect(patch, http.StatusOK, `"revision":2`); err != nil {
		return err
	}
	if _, _, err := expect(get("/"+id+"/revisions", "text/html"), http.StatusOK, "README.md"); err != nil {
		return err
	}
	logger.Debug("self-test passed")
	return nil
}
