package web

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

const inlineScript = `
	import("/_app/immutable/entry/start.abc.js").then((kit) => kit.start());
`

const testIndex = `<!doctype html><html><head>
<script src="/_app/immutable/entry/polyfill.js"></script>
</head><body><div style="display: contents"><script>` + inlineScript + `</script></div></body></html>`

// buildDir lays out a minimal app build on disk (the embedded one only
// exists after `make build`).
func buildDir(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":                        testIndex,
		"service-worker.js":                 "// sw",
		"manifest.webmanifest":              "{}",
		"favicon.svg":                       "<svg/>",
		"_app/version.json":                 `{"version":"1"}`,
		"_app/immutable/entry/start.abc.js": "export {}",
		"_app/immutable/assets/app.abc.css": "body{}",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{PublicBaseURL: "https://bees.example.org"}
	cfg.Web.Serve = true
	cfg.Web.Dir = dir
	return cfg
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestStaticFilesGetCacheHeaders(t *testing.T) {
	h, err := Handler(buildDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/_app/immutable/entry/start.abc.js": "public, max-age=31536000, immutable",
		"/_app/immutable/assets/app.abc.css": "public, max-age=31536000, immutable",
		"/":                                  "no-cache",
		"/service-worker.js":                 "no-cache",
		"/_app/version.json":                 "no-cache",
		"/hives/123":                         "no-cache", // client route: the shell
		"/favicon.svg":                       "public, max-age=3600",
		"/manifest.webmanifest":              "public, max-age=3600",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
	// The shell is what a client route and "/" return.
	if body := get(t, h, "/hives/123").Body.String(); body != testIndex {
		t.Errorf("client route did not return the app shell")
	}
	// A directory is not listed; it falls through to the shell.
	if rec := get(t, h, "/_app/immutable/"); rec.Body.String() != testIndex {
		t.Errorf("directory request must return the shell, got %q", rec.Body.String())
	}
}

func TestContentSecurityPolicy(t *testing.T) {
	cfg := buildDir(t)
	cfg.Maps = config.MapsConfig{Enabled: true,
		TileOrigin: "https://*.tile.openstreetmap.org", GeocoderOrigin: "https://nominatim.openstreetmap.org"}
	index, err := IndexHTML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(inlineScript))
	hash := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	if got := InlineScriptHashes(index); len(got) != 1 || got[0] != hash {
		t.Fatalf("inline script hashes: %v, want exactly [%s] (the src= script is not inline)", got, hash)
	}

	want := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'wasm-unsafe-eval' '" + hash + "'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob: https://*.tile.openstreetmap.org",
		"font-src 'self'",
		"connect-src 'self' https://nominatim.openstreetmap.org https://*.tile.openstreetmap.org",
		"worker-src 'self' blob:",
		"manifest-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
	if got := ContentSecurityPolicy(cfg, index); got != want {
		t.Fatalf("policy:\n got %s\nwant %s", got, want)
	}

	// Maps off: no remote origin at all. No app shell: no hash, no inline script.
	cfg.Maps = config.MapsConfig{}
	got := ContentSecurityPolicy(cfg, nil)
	if strings.Contains(got, "http") || strings.Contains(got, "sha256-") ||
		!strings.Contains(got, "script-src 'self' 'wasm-unsafe-eval';") || !strings.Contains(got, "connect-src 'self';") {
		t.Fatalf("policy without maps and shell: %s", got)
	}
	for _, forbidden := range []string{"'unsafe-eval'", "script-src 'self' 'unsafe-inline'", " * ", "*;"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("policy must not contain %q: %s", forbidden, got)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	cfg := buildDir(t)
	index, _ := IndexHTML(cfg)
	app, err := Handler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", app)
	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })

	h := SecurityHeaders(cfg, index)(mux)
	for _, path := range []string{"/", "/api", "/_app/immutable/entry/start.abc.js", "/service-worker.js"} {
		hdr := get(t, h, path).Header()
		if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Referrer-Policy") != "no-referrer" ||
			hdr.Get("Strict-Transport-Security") != "max-age=31536000" {
			t.Errorf("%s: headers %v", path, hdr)
		}
		csp := hdr.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "'sha256-") {
			t.Errorf("%s: policy %q", path, csp)
		}
	}

	// Plain http (a LAN install): no HSTS, which would lock the browser out.
	cfg.PublicBaseURL = "http://192.168.1.20:8080"
	hdr := get(t, SecurityHeaders(cfg, index)(mux), "/").Header()
	if hdr.Get("Strict-Transport-Security") != "" || hdr.Get("Content-Security-Policy") == "" {
		t.Errorf("http instance: headers %v", hdr)
	}
}

func TestHostGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cfg := &config.Config{PublicBaseURL: "https://Bees.example.org"}
	cfg.AllowedHosts = []string{"hive.lan", "Pi.local:8080"}
	guard := HostGuard(cfg)(ok)

	for host, want := range map[string]int{
		"localhost":              http.StatusNoContent,
		"localhost:8080":         http.StatusNoContent,
		"app.localhost:8080":     http.StatusNoContent,
		"127.0.0.1:8080":         http.StatusNoContent,
		"192.168.1.20":           http.StatusNoContent,
		"[::1]:8080":             http.StatusNoContent,
		"[fd00::1]":              http.StatusNoContent,
		"bees.example.org":       http.StatusNoContent, // host of the public base URL
		"BEES.example.org:443":   http.StatusNoContent,
		"bees.example.org.":      http.StatusNoContent,
		"hive.lan:8080":          http.StatusNoContent, // BEEHIVE_ALLOWED_HOSTS
		"pi.local":               http.StatusNoContent,
		"evil.example":           http.StatusForbidden, // a rebinding attacker's name
		"localhost.evil.example": http.StatusForbidden,
		"127.0.0.1.evil.example": http.StatusForbidden,
		"sub.bees.example.org":   http.StatusForbidden,
		"":                       http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, rec.Code, want)
		}
		if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "BEEHIVE_ALLOWED_HOSTS") {
			t.Errorf("Host %q: the rejection must name the setting: %s", host, rec.Body)
		}
	}

	// "*" switches the check off.
	cfg.AllowedHosts = []string{"*"}
	req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	HostGuard(cfg)(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("wildcard: got %d", rec.Code)
	}
}

// The server bounds every request with read and write timeouts; a streaming
// RPC must outlive them, over HTTP/1.1 and over cleartext HTTP/2.
func TestNoDeadlineKeepsStreamsOpenPastTheTimeouts(t *testing.T) {
	const timeout = 150 * time.Millisecond
	stream := func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for i := range 4 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(timeout):
			}
			if _, err := fmt.Fprintf(w, "tick %d\n", i); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", stream)
	mux.HandleFunc("/bounded", stream)

	for _, proto := range []string{"HTTP/1.1", "h2c"} {
		t.Run(proto, func(t *testing.T) {
			srv := httptest.NewUnstartedServer(NoDeadline(map[string]bool{"/stream": true})(mux))
			srv.Config.ReadTimeout = timeout
			srv.Config.WriteTimeout = timeout
			protocols := new(http.Protocols)
			protocols.SetHTTP1(true)
			protocols.SetUnencryptedHTTP2(true)
			srv.Config.Protocols = protocols
			srv.Start()
			defer srv.Close()

			transport := &http.Transport{}
			if proto == "h2c" {
				clientProtocols := new(http.Protocols)
				clientProtocols.SetUnencryptedHTTP2(true)
				transport.Protocols = clientProtocols
			}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}

			read := func(path string) string {
				resp, err := client.Get(srv.URL + path)
				if err != nil {
					return "error: " + err.Error()
				}
				defer func() { _ = resp.Body.Close() }()
				if proto == "h2c" && resp.ProtoMajor != 2 {
					t.Fatalf("expected HTTP/2, got %s", resp.Proto)
				}
				body, _ := io.ReadAll(resp.Body)
				return string(body)
			}
			if got := read("/stream"); got != "tick 0\ntick 1\ntick 2\ntick 3\n" {
				t.Errorf("stream was cut by the server timeouts: %q", got)
			}
			// The same handler without the exemption is cut off.
			if got := read("/bounded"); got == "tick 0\ntick 1\ntick 2\ntick 3\n" {
				t.Errorf("a non-streaming route outlived the write timeout: %q", got)
			}
		})
	}
}
