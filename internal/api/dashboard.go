package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// StaticDashboard serves a built dashboard from fsys (the contents of web/dist). Unknown paths
// without a file extension fall back to index.html (client routes). It returns nil when
// index.html is missing, so New serves the "not built" page instead.
func StaticDashboard(fsys fs.FS) http.Handler {
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		return nil
	}
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && path.Ext(name) == "" {
			if _, err := fs.Stat(fsys, name); err != nil {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/"
				files.ServeHTTP(w, r2)
				return
			}
		}
		if name == "" || name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
