//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secretfile

import (
	"fmt"
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

func validatePlatformIntegrity(permissions os.FileMode) error {
	if permissions&0o133 != 0 {
		return fmt.Errorf("secret file permissions are unsafe: %04o", permissions)
	}
	return nil
}
