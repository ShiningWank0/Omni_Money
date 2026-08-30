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
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, root, nil
}
