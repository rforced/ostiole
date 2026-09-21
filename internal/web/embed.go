// Package web serves the embedded Vue single-page application.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist is populated by `task web:build` (Vite writes to internal/web/dist).
//
//go:embed all:dist
var dist embed.FS

// Handler serves the embedded UI built into the binary.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return HandlerFS(sub)
}

// HandlerFS serves a built SPA from fsys. Paths that do not match a file fall
// back to index.html so client-side routing works on deep links. If the
// bundle is missing (frontend not built) it returns 503 with a hint.
func HandlerFS(fsys fs.FS) http.Handler {
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "ostiole: web UI not built; run `task web:build`", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if info, err := fs.Stat(fsys, p); err == nil && !info.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					// Vite content-hashes everything under assets/.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: serve index.html without redirecting.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
