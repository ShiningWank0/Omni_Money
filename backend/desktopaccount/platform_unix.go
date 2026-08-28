//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package desktopaccount

import (
	"os"
	"syscall"
)

func hasInsecurePermissions(mode os.FileMode) bool {
	return mode.Perm()&0177 != 0
}

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func createPrivateTemp(directory, pattern string) (*os.File, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil { // #nosec G302 -- secrets must be owner-only.
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return file, nil
}

func hardenPrivateDirectory(path string) error {
	return os.Chmod(path, 0700) // #nosec G302 -- financial data must be owner-only.
}

func replaceFileAtomic(source, target string) error {
	return os.Rename(source, target)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path) // #nosec G304 -- manifest parent is the validated private Desktop data root.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
