//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package audithmac

import "os"

func syncDirectory(path string) error {
	// #nosec G304 -- path is the parent of the operator-selected keyring file;
	// opening it is required to fsync the directory after an atomic rename.
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
