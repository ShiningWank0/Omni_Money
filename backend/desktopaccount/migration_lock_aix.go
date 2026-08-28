//go:build aix

package desktopaccount

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireMigrationLock(root string) (func(), error) {
	path := filepath.Join(root, migrationLockFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		_ = file.Close()
	}, nil
}
