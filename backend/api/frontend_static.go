package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
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
		contentType := ""
		switch r.URL.Path {
		case "/", "/index.html", "/login", "/login/":
			name, contentType = "index.html", "text/html; charset=utf-8"
		case "/favicon.svg":
			contentType = "image/svg+xml"
		default:
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				contentType = frontendAssetContentType(path.Ext(name))
			}
		}
		if contentType == "" || !fs.ValidPath(name) || strings.Contains(name, `\`) {
			http.NotFound(w, r)
			return
		}
		for _, segment := range strings.Split(name, "/") {
			if strings.HasPrefix(segment, ".") {
				http.NotFound(w, r)
				return
			}
		}

		// OpenInRoot confines symlink resolution as well as ordinary paths. A
		// separate Clean/EvalSymlinks check would leave a check/open race.
		file, err := os.OpenInRoot(root, name)
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

func frontendAssetContentType(extension string) string {
	switch extension {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	default:
		return ""
	}
}
