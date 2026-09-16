package ui

import (
	"net/http"
	"strings"
)

// staticHandler serves the embedded asset tree under the UI's static prefix.
//
// Every asset ships inside the binary, so it is versioned rather than
// timestamped: an embed.FS has a zero ModTime, which makes Last-Modified and
// ETag either wrong or useless. The URL carries ?v=<assetVersion> instead, and
// since that version changes whenever any asset changes, the response can be
// cached forever.
//
// Assets are not secret, but the route is registered behind the same gate as
// every other /admin/ui path: an unauthenticated peer should not be able to
// enumerate the asset set any more than the data.
func (h *Handler) StaticHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(r.URL.Path, "/admin/ui/static/") {
		if v := r.URL.Query().Get("v"); v != "" && v == h.assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
	}
	http.StripPrefix("/admin/ui/static/", http.FileServerFS(h.assetsFS)).ServeHTTP(w, r)
}

// The layout builds its own asset URLs (app.css?v={{.AssetVersion}} and
// htmx.min.js?v={{.AssetVersion}}) rather than calling a helper: there is no
// asset URL constructed in Go, and adding a helper for one that does not exist
// invites a second scheme that can disagree with the template's.
