//go:build !windows

package database

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

func openSnapshotFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- path is a validated snapshot basename under a private directory.
}

func openDurableDatabaseFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0) // #nosec G304 -- caller passes a generated private database artifact.
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !validSnapshotFile(info) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("durability target is not a private regular file")
	}
	return file, nil
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
	// Compare the decimal representations to avoid narrowing the process UID
	// (and the corresponding gosec G115 warning) on platforms whose stat UID is
	// narrower than int.
	return strconv.FormatUint(uint64(stat.Uid), 10) == strconv.Itoa(uid) && stat.Nlink == 1
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

// installRecoveryFile is used when the live pathname was lost entirely.  The
// generated recovery image is already validated and private; rename keeps the
// publication atomic on POSIX.
func installRecoveryFile(replacement, target, _ string) error {
	return os.Rename(replacement, target)
}

func replaceManifestFile(replacement, target string) error {
	return os.Rename(replacement, target)
}

// publishSnapshotFile atomically exposes a fully validated staging image.
func publishSnapshotFile(replacement, target string) error {
	return os.Rename(replacement, target)
}
