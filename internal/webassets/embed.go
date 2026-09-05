// Package webassets embeds the built React bundle so the Go binary serves the
// SPA directly. The real bundle is produced by `npm --prefix web run build`
// into ./dist (see Makefile); a placeholder index keeps `go build` working
// before the first frontend build.
package webassets

import (
	"io/fs"
	"net/http"
	"strings"

	"embed"
)

//go:embed all:dist
var distFS embed.FS

const fallbackHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Tiny Password</title></head>
<body><p>UI assets are not built. Run <code>make web-build</code>.</p></body></html>
`

// SPAHandler serves hashed build assets and falls back to the SPA entry for
// client-side routes. API paths never reach this handler.
func SPAHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("webassets: embedded dist missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		f, err := sub.Open(p)
		if err != nil {
			serveFallback(w, r)
			return
		}
		st, err := f.Stat()
		_ = f.Close()
		if err != nil || st.IsDir() {
			serveFallback(w, r)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			// Content-hashed filenames are immutable.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveFallback(w http.ResponseWriter, _ *http.Request) {
	if _, err := fs.Stat(distFS, "dist/index.html"); err == nil {
		// Real bundle present: client-side route → serve the SPA entry.
		index, _ := distFS.ReadFile("dist/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(index)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fallbackHTML))
}
