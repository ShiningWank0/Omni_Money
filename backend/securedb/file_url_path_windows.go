package securedb

import (
	"path/filepath"
	"strings"
)

func databaseFileURLPath(path string) string {
	uriPath := filepath.ToSlash(path)
	volume := filepath.VolumeName(path)
	if len(volume) == 2 && volume[1] == ':' && strings.HasPrefix(uriPath[len(volume):], "/") {
		// A drive-letter absolute path must be represented as file:///C:/...
		// rather than file:C:/...; SQLite interprets the latter's drive letter
		// as a URI authority and rejects the database before applying its key.
		return "/" + uriPath
	}
	return uriPath
}
