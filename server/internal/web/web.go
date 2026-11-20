// Package web serves the built SvelteKit SPA so production is a single binary.
// The real assets are copied into ./dist by `make build` (the embedded
// placeholder keeps `go build` working in dev, where you run vite separately).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/openbeehive-org/openbeehive-app/server/internal/config"
)

//go:embed all:dist
var embedded embed.FS

// distFS returns the app build: cfg.Web.Dir when set (handy for testing a
// fresh build without recompiling the binary), otherwise the embedded one.
func distFS(cfg *config.Config) (fs.FS, error) {
	if cfg.Web.Dir != "" {
		return os.DirFS(cfg.Web.Dir), nil
	}
	return fs.Sub(embedded, "dist")
}

// IndexHTML returns the app shell, or an error when the app is not built.
func IndexHTML(cfg *config.Config) ([]byte, error) {
	dist, err := distFS(cfg)
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(dist, "index.html")
}

// Handler serves the SPA: real files (hashed assets, icons, manifest) are
// served directly; every other path falls back to index.html so the client
// router handles it.
func Handler(cfg *config.Config) (http.Handler, error) {
	dist, err := distFS(cfg)
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err // dist not built yet
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// Only files are served; a directory falls through to the app shell
		// rather than a listing.
		if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
			w.Header().Set("Cache-Control", cacheControl(name))
			files.ServeHTTP(w, r)
			return
		}
		// SPA fallback (no caching for the shell so deploys take effect).
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}), nil
}

// cacheControl picks the caching rule for a built file. Files under
// _app/immutable carry a content hash in their name and never change. The
// shell, the service worker and the version marker are what tell a browser
// that a new build exists, so they are always revalidated. Icons, fonts and
// the manifest keep their names across builds and get a short lifetime.
func cacheControl(name string) string {
	switch {
	case strings.HasPrefix(name, "_app/immutable/"):
		return "public, max-age=31536000, immutable"
	case name == "index.html", name == "service-worker.js", name == "_app/version.json":
		return "no-cache"
	default:
		return "public, max-age=3600"
	}
}
