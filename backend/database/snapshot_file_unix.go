//go:build !windows

package database

import (
	"os"
	"syscall"
)

func openSnapshotFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- path is a validated snapshot basename under a private directory.
}

// validSnapshotFile deliberately uses Lstat metadata.  A restore source must
// be an owner-created, single-link regular file; following a symlink or a
// hard-link would let an attacker change the object after validation.
func validSnapshotFile(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	// Snapshot files are private and immutable from other users.  Read-only
	// legacy snapshots (0644) remain importable for the desktop migration path,
	// while any group/other write permission is rejected.
	if info.Mode().Perm()&0022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	return stat.Nlink == 1
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- caller passes the configured private directory.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
