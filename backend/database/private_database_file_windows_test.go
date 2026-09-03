//go:build windows

package database

import (
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/fileprivacy"
)

func TestWindowsPreparePrivateDatabaseFileSetsCurrentOwner(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.db")
			if existing {
				if err := os.WriteFile(path, nil, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if err := preparePrivateDatabaseFile(path); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := fileprivacy.ValidatePrivateFile(file); err != nil {
				t.Fatalf("prepared database placeholder failed owner/DACL validation: %v", err)
			}
		})
	}
}
