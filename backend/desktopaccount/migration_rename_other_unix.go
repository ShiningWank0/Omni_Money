//go:build aix || dragonfly || freebsd || netbsd || openbsd || solaris

package desktopaccount

import "os"

// Hard-link then unlink preserves no-replace semantics on platforms without a
// renameat2-style API. Resume handles the brief crash state where both names
// refer to the same inode.
func renameNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		return err
	}
	return nil
}
