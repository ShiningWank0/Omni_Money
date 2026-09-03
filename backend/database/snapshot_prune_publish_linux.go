//go:build linux

package database

import "golang.org/x/sys/unix"

// publishSnapshotPruneManifestNoReplace atomically makes a fully synced
// journal visible without ever replacing unresolved transaction state.
func publishSnapshotPruneManifestNoReplace(replacement, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, replacement, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}
