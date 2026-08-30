//go:build !server

package main

import (
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/fileprivacy"
)

func TestPrepareDesktopSQLCipherSelfTestStorageIsPrivateBeforeOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "self-test")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "probe.db")
	if err := prepareDesktopSQLCipherSelfTestStorage(directory, databasePath); err != nil {
		t.Fatal(err)
	}
	if err := fileprivacy.ValidateDirectory(directory); err != nil {
		t.Fatalf("self-test directory is not protected: %v", err)
	}
	file, err := os.Open(databasePath) // #nosec G304 -- test-owned exact path.
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		t.Fatalf("self-test placeholder is not protected before SQLCipher open: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("self-test placeholder size=%d, want empty pre-open file", info.Size())
	}
	if err := prepareDesktopSQLCipherSelfTestStorage(directory, databasePath); !os.IsExist(err) {
		t.Fatalf("second preparation error=%v, want exclusive-create collision", err)
	}
}
