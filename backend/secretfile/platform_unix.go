//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secretfile

import (
	"os"
	"syscall"
)

func secureOpen(path string, _ policy) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validatePlatformConfidential(_ string) error { return nil }
