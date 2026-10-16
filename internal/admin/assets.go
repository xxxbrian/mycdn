package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var assets embed.FS

func frontend() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServerFS(root)
	_, built := fs.Stat(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if built != nil {
			http.Error(w, "Frontend not built. Run make build, or use the Vite development server.", http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if _, err := fs.Stat(root, strings.TrimPrefix(r.URL.Path, "/")); err != nil && !strings.Contains(r.URL.Path, ".") {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
