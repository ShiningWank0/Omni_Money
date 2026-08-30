//go:build !windows

package database

import (
	"os"

	"omni_money/backend/fileprivacy"
)

func hardenPrivateFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0600) // #nosec G304 -- caller has already validated the private database path.
	if err != nil {
		return err
	}
	defer file.Close()
	return fileprivacy.Harden(file)
}
