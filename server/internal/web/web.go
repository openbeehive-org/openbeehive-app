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

// Handler serves the SPA: real files (hashed assets, icons, manifest) are
// served directly; every other path falls back to index.html so the client
// router handles it. cfg.Web.Dir overrides the embedded build with a directory
// on disk (handy for testing a fresh build without recompiling the binary).
func Handler(cfg *config.Config) (http.Handler, error) {
	var dist fs.FS
	if cfg.Web.Dir != "" {
		dist = os.DirFS(cfg.Web.Dir)
	} else {
		sub, err := fs.Sub(embedded, "dist")
		if err != nil {
			return nil, err
		}
		dist = sub
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
		if f, err := dist.Open(name); err == nil {
			_ = f.Close()
			files.ServeHTTP(w, r)
			return
		}
		// SPA fallback (no caching for the shell so deploys take effect).
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}), nil
}
