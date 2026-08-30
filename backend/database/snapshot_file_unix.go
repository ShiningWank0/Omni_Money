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
	uid := os.Getuid()
	if !ok || uid < 0 {
		return false
	}
	ownerUID := uint64(stat.Uid)
	// #nosec G115 -- uid is checked non-negative before this widening
	// conversion; Unix process UIDs are represented in this range.
	return ownerUID == uint64(uid) && stat.Nlink == 1
}

func snapshotModeAllowed(info os.FileInfo, encrypted bool) bool {
	if info == nil {
		return false
	}
	if encrypted {
		return info.Mode().Perm() == 0600
	}
	// Desktop migration may encounter read-only legacy snapshots. They cannot
	// be modified by another user, while group/other write access is rejected.
	return info.Mode().Perm()&0022 == 0
}

func snapshotDirectoryModeAllowed(info os.FileInfo) bool {
	return info != nil && info.Mode().Perm() == 0700
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- caller passes the configured private directory.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// replaceDatabaseFile keeps the live pathname continuously present. POSIX
// rename replaces the destination atomically; backupPath is already a durable
// copy used if post-swap validation fails.
func replaceDatabaseFile(replacement, target, _ string) error {
	return os.Rename(replacement, target)
}
