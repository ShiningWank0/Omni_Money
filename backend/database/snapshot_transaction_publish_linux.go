//go:build linux

package database

import "golang.org/x/sys/unix"

func (lock *snapshotTransactionLock) publishManifestNoReplace(replacement, target string) error {
	replacementName, err := lock.directName(replacement)
	if err != nil {
		return err
	}
	targetName, err := lock.directName(target)
	if err != nil {
		return err
	}
	return unix.Renameat2(int(lock.directory.Fd()), replacementName, int(lock.directory.Fd()), targetName, unix.RENAME_NOREPLACE)
}
