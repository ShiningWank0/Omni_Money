package api

import (
	"net/http"
	"strings"

	"omni_money/backend/publicassets"
)

func registerFrontendRoutes(mux *http.ServeMux) {
	frontend := frontendFileHandler("frontend/dist")
	mux.Handle("/", frontend)
	mux.Handle("/login", frontend)
	mux.Handle("/login/", frontend)
}

// Public frontend files are read-only build outputs. Do not expose a general
// filesystem: http.Dir follows links outside its root and lists directories.
// No request value is used to fetch, decode, upload, or execute an image.
func frontendFileHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/")
		switch r.URL.Path {
		case "/", "/index.html", "/login", "/login/":
			name = "index.html"
		}
		contentType := publicassets.ContentType(name)
		if contentType == "" {
			http.NotFound(w, r)
			return
		}
		file, err := publicassets.Open(root, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
}
