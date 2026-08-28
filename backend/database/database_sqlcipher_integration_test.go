package database

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/securedb"
)

func TestEncryptedDatabaseSnapshotAndRestoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	snapshotDir := filepath.Join(dir, "snapshots")
	key := databaseTestKey(0x61)

	if err := InitEncryptedDB(path, key); err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" &&
			(errors.Is(err, securedb.ErrCipherUnavailable) ||
				errors.Is(err, securedb.ErrCipherVersion) ||
				errors.Is(err, securedb.ErrCipherProvider)) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(CloseDB)

	const before = "encrypted-lifecycle-before"
	const after = "encrypted-lifecycle-after"
	if _, err := GetDB().Exec("INSERT INTO settings(key, value) VALUES('marker', ?)", before); err != nil {
		t.Fatal(err)
	}
	snapshotPath, err := CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	assertDatabaseCiphertext(t, path, before)
	assertDatabaseCiphertext(t, snapshotPath, before)

	if _, err := GetDB().Exec("UPDATE settings SET value=? WHERE key='marker'", after); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSnapshot(snapshotDir, filepath.Base(snapshotPath)); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := GetDB().QueryRow("SELECT value FROM settings WHERE key='marker'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != before {
		t.Fatalf("restored value=%q, want %q", value, before)
	}
	assertDatabaseCiphertext(t, path, before)

	CloseDB()
	if err := InitEncryptedDB(path, databaseTestKey(0x62)); err == nil {
		CloseDB()
		t.Fatal("database opened with the wrong key")
	}
	if err := InitEncryptedDB(path, key); err != nil {
		t.Fatalf("reopen with correct key: %v", err)
	}
}

func databaseTestKey(value byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = value
	}
	return key
}

func assertDatabaseCiphertext(t *testing.T, path, marker string) {
	t.Helper()
	if err := securedb.RequireEncryptedHeader(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(marker)) {
		t.Fatalf("plaintext marker found in %s", filepath.Base(path))
	}
}
