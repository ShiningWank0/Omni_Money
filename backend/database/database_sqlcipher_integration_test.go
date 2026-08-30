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

func TestEncryptedSnapshotRestoreRejectsWrongKeyAndCorruption(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first", "ledger.db")
	secondPath := filepath.Join(root, "second", "ledger.db")
	first, err := OpenEncryptedInstance(firstPath, databaseTestKey(0x71))
	if shouldSkipSQLCipherTest(err) {
		t.Skipf("SQLCipher integration build is not active: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenEncryptedInstance(secondPath, databaseTestKey(0x82))
	if err != nil {
		_ = first.Close()
		if shouldSkipSQLCipherTest(err) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})
	if _, err := first.DB().Exec("INSERT INTO settings(key, value) VALUES('marker', 'first-live')"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.DB().Exec("INSERT INTO settings(key, value) VALUES('marker', 'second-live')"); err != nil {
		t.Fatal(err)
	}
	foreignSnapshot, err := second.CreateSnapshot("")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RestoreSnapshot(filepath.Dir(foreignSnapshot), filepath.Base(foreignSnapshot)); err == nil {
		t.Fatal("snapshot encrypted with another vault key was accepted")
	}
	assertEncryptedSetting(t, first, "first-live")

	ownedSnapshot, err := first.CreateSnapshot("")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(ownedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) < 64 {
		t.Fatalf("encrypted snapshot unexpectedly short: %d", len(content))
	}
	content[48] ^= 0x5a
	if err := os.WriteFile(ownedSnapshot, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.RestoreSnapshot(filepath.Dir(ownedSnapshot), filepath.Base(ownedSnapshot)); err == nil {
		t.Fatal("corrupted encrypted snapshot was accepted")
	}
	assertEncryptedSetting(t, first, "first-live")
}

func assertEncryptedSetting(t *testing.T, instance *Instance, want string) {
	t.Helper()
	var got string
	if err := instance.DB().QueryRow("SELECT value FROM settings WHERE key='marker'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("live encrypted value=%q, want %q", got, want)
	}
}

func shouldSkipSQLCipherTest(err error) bool {
	return err != nil && os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" &&
		(errors.Is(err, securedb.ErrCipherUnavailable) ||
			errors.Is(err, securedb.ErrCipherVersion) ||
			errors.Is(err, securedb.ErrCipherProvider))
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
