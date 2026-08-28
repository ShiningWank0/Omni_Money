//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package desktopaccount

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireMigrationLock(root string) (func(), error) {
	path := filepath.Join(root, migrationLockFileName)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap Desktop migration lock file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || hasInsecurePermissions(info.Mode()) {
		_ = file.Close()
		return nil, errors.New("Desktop migration lock must be an owner-only regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
