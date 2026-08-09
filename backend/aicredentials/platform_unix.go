//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package aicredentials

import (
	"os"
	"syscall"
)

func secureOpen(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func hasInsecurePermissions(mode os.FileMode) bool {
	return mode.Perm()&0o033 != 0
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
