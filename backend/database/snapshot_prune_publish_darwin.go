//go:build darwin

package database

import "golang.org/x/sys/unix"

// publishSnapshotPruneManifestNoReplace atomically makes a fully synced
// journal visible without ever replacing unresolved transaction state.
func publishSnapshotPruneManifestNoReplace(replacement, target string) error {
	return unix.RenamexNp(replacement, target, unix.RENAME_EXCL)
}
