// Package ui serves the compiled single-page application.
//
// The frontend is embedded into the binary, so a deployment is one container
// with no web server in front of it and no static volume to mount.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The build pipeline copies the Vite output into dist/ before compiling.
// A placeholder index.html is committed so that `go build` also works in a
// checkout where the frontend has not been built.
//
//go:embed all:dist
var embedded embed.FS

// Handler serves the SPA: real files are returned as-is, everything else falls
// back to index.html so that client-side routes survive a page reload.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("ui: embedded assets are missing: " + err.Error())
	}
	files := http.FS(sub)
	fileServer := http.FileServer(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if f, err := files.Open(path); err == nil {
			defer f.Close()
			// Hashed asset filenames may be cached indefinitely; index.html must
			// not be, otherwise clients keep loading an old bundle after upgrade.
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// Unknown path: hand it to the router in the browser.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
