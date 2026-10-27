// Package publicassets confines public frontend reads and validates build outputs.
package publicassets

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Open pins each directory and rejects links at open time, including the root.
// Ancestors of the operator-selected root are trusted; request paths are not.
func Open(root, name string) (*os.File, error) {
	if !validName(name) {
		return nil, fs.ErrNotExist
	}
	// Remove a trailing separator so it cannot make the OS follow a linked
	// root before applying the final-component no-follow flag.
	file, err := openNoFollow(filepath.Clean(root), strings.Split(name, "/"))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}

func validName(name string) bool {
	if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// ContentType is the shared serving/packaging allowlist. A manifest cannot
// authorize hidden files, source maps, databases, configuration or executables.
func ContentType(name string) string {
	if !validName(name) {
		return ""
	}
	if name == "index.html" {
		return "text/html; charset=utf-8"
	}
	if name == "favicon.svg" {
		return "image/svg+xml"
	}
	if !strings.HasPrefix(name, "assets/") {
		return ""
	}
	switch path.Ext(name) {
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
