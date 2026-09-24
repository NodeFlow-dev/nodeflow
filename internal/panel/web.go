package panel

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webAssets is populated from frontend/dist by Dockerfile.panel or
// scripts/build-panel.sh. The checked-in production bundle keeps local Go
// builds and backend tests self-contained.
//
//go:embed web_dist/*
var webAssets embed.FS

func embeddedWebHandler() http.Handler {
	root, err := fs.Sub(webAssets, "web_dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested == "." || requested == "" {
			requested = "index.html"
		}
		if info, statErr := fs.Stat(root, requested); statErr == nil && !info.IsDir() {
			if hashedAssetPath(requested) {
				// Content-hashed build output never changes under the same
				// name; index.html and API responses stay no-store.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		// Client-side routes receive the application shell. Asset-like missing
		// paths remain 404 so broken bundles do not get HTML with a 200 status.
		if strings.Contains(path.Base(requested), ".") {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

// hashedAssetPath matches Vite build output such as assets/index-C4b8adjg.js:
// a file directly under assets/ whose base name ends in -<hash> with a hash of
// 8 base64url characters. The hash itself may contain '-' or '_'
// (index-DyWCGJ-L.js), so it is taken as the last 8 characters of the stem
// rather than the text after the last dash.
func hashedAssetPath(requested string) bool {
	const viteHashLength = 8
	name, ok := strings.CutPrefix(requested, "assets/")
	if !ok || strings.Contains(name, "/") {
		return false
	}
	ext := path.Ext(name)
	if ext == "" {
		return false
	}
	stem := strings.TrimSuffix(name, ext)
	if len(stem) < viteHashLength+2 || stem[len(stem)-viteHashLength-1] != '-' {
		return false
	}
	hash := stem[len(stem)-viteHashLength:]
	for _, char := range hash {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}
