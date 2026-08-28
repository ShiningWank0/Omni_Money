package securedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

const testSecret = "omni-sqlcipher-plaintext-canary-8d85b11e"

func requireSQLCipher(t *testing.T, path string, key RawKey) (*Opener, *sql.DB) {
	t.Helper()
	opener := NewEncryptedOpener(key)
	db, err := opener.Open(context.Background(), path, Writable)
	if err == nil {
		return opener, db
	}
	opener.Destroy()
	if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" &&
		(errors.Is(err, ErrCipherUnavailable) || errors.Is(err, ErrCipherVersion) || errors.Is(err, ErrCipherProvider)) {
		t.Skipf("SQLCipher integration build is not active: %v", err)
	}
	t.Fatalf("open SQLCipher database: %v", err)
	return nil, nil
}

func fixedKey(value byte) RawKey {
	var key RawKey
	for index := range key {
		key[index] = value
	}
	return key
}

func TestEncryptedDatabaseAndSnapshotFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.db")
	key := fixedKey(0x31)
	opener, db := requireSQLCipher(t, path, key)
	if _, err := db.Exec("CREATE TABLE secrets (id INTEGER PRIMARY KEY, value TEXT, payload BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO secrets(value, payload) VALUES (?, ?)", testSecret, []byte(testSecret)); err != nil {
		t.Fatal(err)
	}
	if err := opener.CheckIntegrity(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(dir, "snapshot.db")
	if err := opener.Backup(context.Background(), db, snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertEncryptedFiles(t, path)
	assertEncryptedFiles(t, snapshotPath)

	plain, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.QueryRow("SELECT count(*) FROM sqlite_schema").Scan(new(int)); err == nil {
		_ = plain.Close()
		t.Fatal("encrypted database opened without a key")
	}
	_ = plain.Close()

	wrong := NewEncryptedOpener(fixedKey(0x32))
	if wrongDB, err := wrong.Open(context.Background(), path, Writable); err == nil {
		_ = wrongDB.Close()
		wrong.Destroy()
		t.Fatal("encrypted database opened with the wrong key")
	}
	wrong.Destroy()

	reopened, err := opener.Open(context.Background(), path, Writable)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.QueryRow("SELECT value FROM secrets").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != testSecret {
		t.Fatal("decrypted value differs")
	}
}

func TestEnsureEncryptedMigratesPlaintextAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	plain, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec("CREATE TABLE ledger (id INTEGER PRIMARY KEY, note TEXT, receipt BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec("INSERT INTO ledger(note, receipt) VALUES (?, ?)", testSecret, []byte(testSecret)); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec("PRAGMA user_version = 17"); err != nil {
		t.Fatal(err)
	}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}

	key := fixedKey(0x55)
	opener, probe := requireSQLCipher(t, filepath.Join(dir, "probe.db"), key)
	_ = probe.Close()
	if err := EnsureEncrypted(context.Background(), path, opener); err != nil {
		t.Fatal(err)
	}
	assertEncryptedFiles(t, path)
	db, err := opener.Open(context.Background(), path, Writable)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var note string
	var receipt []byte
	var userVersion int
	if err := db.QueryRow("SELECT note, receipt FROM ledger WHERE id=1").Scan(&note, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if note != testSecret || string(receipt) != testSecret || userVersion != 17 {
		t.Fatalf("migration changed data: note=%q receipt=%q version=%d", note, receipt, userVersion)
	}
}

func assertEncryptedFiles(t *testing.T, path string) {
	t.Helper()
	if err := RequireEncryptedHeader(path); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		content, err := os.ReadFile(path + suffix) // #nosec G304 -- test-owned temporary files.
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte(testSecret)) || strings.Contains(string(content), sqlitePlaintextHeader) {
			t.Fatalf("plaintext found in %s", filepath.Base(path+suffix))
		}
	}
}
