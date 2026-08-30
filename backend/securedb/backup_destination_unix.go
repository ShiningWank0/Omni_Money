//go:build !windows

package securedb

import (
	"errors"
	"os"
	"syscall"

	"omni_money/backend/fileprivacy"
)

// assertBackupDestination opens the destination without following a symlink
// and binds the pathname to the already-created placeholder inode. A plain
// os.Stat is not sufficient here: a symlink to the placeholder would make
// os.SameFile succeed while the SQL driver followed an attacker-controlled
// pathname during the backup.
func assertBackupDestination(path string, expected os.FileInfo) error {
	file, err := openNoFollowPrivateFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if actual.Mode()&os.ModeSymlink != 0 || !actual.Mode().IsRegular() {
		return errors.New("backup destination is not a regular file")
	}
	if expected == nil || !os.SameFile(expected, actual) {
		return errors.New("backup destination was replaced during backup")
	}
	return nil
}

func openNoFollowPrivateFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- caller owns the private database path.
	if err != nil {
		return nil, err
	}
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
