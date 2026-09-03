//go:build !server

package main

import (
	"errors"
	"os"
	"path/filepath"

	"omni_money/backend/fileprivacy"
)

// prepareDesktopSQLCipherSelfTestStorage establishes the final Windows DACL
// and Unix permissions before SQLCipher can write its first database page.
func prepareDesktopSQLCipherSelfTestStorage(directory, databasePath string) error {
	if filepath.Dir(databasePath) != filepath.Clean(directory) {
		return errors.New("self-test database escaped its private directory")
	}
	if err := fileprivacy.HardenDirectory(directory); err != nil {
		return err
	}
	if err := fileprivacy.ValidateDirectory(directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	file, err := fileprivacy.CreateExclusive(root, directory, filepath.Base(databasePath))
	rootCloseErr := root.Close()
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(databasePath)
		}
	}()
	if rootCloseErr != nil {
		return rootCloseErr
	}
	if err := fileprivacy.Harden(file); err != nil {
		return err
	}
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
