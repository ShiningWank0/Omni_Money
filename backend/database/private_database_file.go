package database

import (
	"fmt"
	"os"
	"path/filepath"

	"omni_money/backend/fileprivacy"
)

// preparePrivateDatabaseFile gives a new placeholder its final owner and ACL
// before SQLite can write a page. Existing ledgers are reopened through the
// platform no-follow hardening boundary and rebound to the same object.
func preparePrivateDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		directory := filepath.Dir(path)
		root, rootErr := os.OpenRoot(directory)
		if rootErr != nil {
			return rootErr
		}
		file, createErr := fileprivacy.CreateExclusive(root, directory, filepath.Base(path))
		rootCloseErr := root.Close()
		if createErr != nil {
			return createErr
		}
		complete := false
		defer func() {
			_ = file.Close()
			if !complete {
				_ = os.Remove(path)
			}
		}()
		if rootCloseErr != nil {
			return rootCloseErr
		}
		if err := fileprivacy.Harden(file); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		complete = true
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("データベースパスが通常ファイルではありません")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	return hardenPrivateFile(path)
}
