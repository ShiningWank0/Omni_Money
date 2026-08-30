//go:build !windows

package fileprivacy

import "os"

// CreatePrivateTempDir creates an owner-only directory before any temporary
// archive bytes are written. The root handle pins subsequent relative opens.
func CreatePrivateTempDir(prefix string) (string, *os.Root, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	created, statErr := os.Lstat(dir)
	if statErr != nil {
		_ = os.Remove(dir)
		return "", nil, statErr
	}
	// #nosec G302 -- 0700 is intentional: this directory contains plaintext CSV and image bytes.
	if err := os.Chmod(dir, 0700); err != nil {
		_ = removeCreatedPrivateDir(dir, created)
		return "", nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = removeCreatedPrivateDir(dir, created)
		return "", nil, err
	}
	return dir, root, nil
}
