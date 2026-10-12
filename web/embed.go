// Package web embeds the operations dashboard: a single self-contained HTML
// page (no build step, no CDN) that renders the fleet on an SVG map, shows
// KPIs and a severity-ranked alert feed, and follows the engine's SSE stream.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed index.html
var assets embed.FS

// FS returns the embedded static assets.
func FS() fs.FS { return assets }

// Handler serves the dashboard at "/" and the remaining assets by path.
// Unknown paths return 404 so API typos are not silently answered with HTML.
func Handler() http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			data, _ := assets.ReadFile("index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
