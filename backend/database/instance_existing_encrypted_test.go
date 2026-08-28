package database

import (
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/securedb"
)

func TestOpenExistingEncryptedInstanceRejectsMissingAndPlaintextFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if instance, err := OpenExistingEncryptedInstance(missing, strictTestRawKey(0x31)); err == nil {
		_ = instance.Close()
		t.Fatal("missing encrypted database was created during strict open")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("strict open changed missing database state: %v", err)
	}

	plaintext := filepath.Join(t.TempDir(), "plaintext.db")
	if err := os.WriteFile(plaintext, []byte("SQLite format 3\x00plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if instance, err := OpenExistingEncryptedInstance(plaintext, strictTestRawKey(0x42)); err == nil {
		_ = instance.Close()
		t.Fatal("plaintext database was accepted during strict encrypted open")
	}
	content, err := os.ReadFile(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if string(content[:16]) != "SQLite format 3\x00" {
		t.Fatal("strict open modified the rejected plaintext database")
	}
}

func strictTestRawKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}
