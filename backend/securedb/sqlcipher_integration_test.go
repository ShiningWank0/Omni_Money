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

func TestCopyPlaintextToEncryptedPreservesWALAndCompleteLogicalState(t *testing.T) {
	dir := t.TempDir()
	key := fixedKey(0x61)
	opener, probe := requireSQLCipher(t, filepath.Join(dir, "probe.db"), key)
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	defer opener.Destroy()

	sourcePath := filepath.Join(dir, "legacy.db")
	source, err := sql.Open("sqlite3", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA wal_autocheckpoint = 0;
		PRAGMA foreign_keys = ON;
		CREATE TABLE automatic (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL);
		CREATE TABLE composite (
			left_key TEXT,
			right_key INTEGER,
			integer_value INTEGER,
			real_value REAL,
			text_value TEXT,
			blob_value BLOB,
			null_value TEXT,
			PRIMARY KEY(left_key, right_key)
		) WITHOUT ROWID;
		CREATE TABLE audit (message TEXT NOT NULL);
		CREATE INDEX composite_text_idx ON composite(text_value);
		CREATE VIEW composite_view AS SELECT left_key, right_key FROM composite;
		CREATE TRIGGER automatic_audit AFTER INSERT ON automatic BEGIN
			INSERT INTO audit(message) VALUES (NEW.value);
		END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`
		INSERT INTO automatic(value) VALUES ('first'), ('second');
		DELETE FROM automatic WHERE id = 2;
		UPDATE sqlite_sequence SET seq = 99 WHERE name = 'automatic';
		INSERT INTO composite(left_key, right_key, integer_value, real_value, text_value, blob_value, null_value)
		VALUES
			('without', 2, -17, -0.0, 'SQLite format 3', x'000102FF', NULL),
			('rowid', 1, 42, 3.5, '', x'', NULL);
		PRAGMA user_version = 23;
		PRAGMA application_id = 1330466121;
	`); err != nil {
		t.Fatal(err)
	}

	mainBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(sourcePath + "-wal")
	if err != nil {
		t.Fatalf("WAL fixture was not created: %v", err)
	}
	if !bytes.Contains(walBefore, []byte("without")) {
		t.Fatal("latest fixture data is not resident in the WAL")
	}

	destinationPath := filepath.Join(dir, "encrypted.db")
	if err := CopyPlaintextToEncrypted(context.Background(), sourcePath, destinationPath, opener); err != nil {
		t.Fatal(err)
	}
	mainAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	walAfter, err := os.ReadFile(sourcePath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mainBefore, mainAfter) || !bytes.Equal(walBefore, walAfter) {
		t.Fatal("read-only migration modified the plaintext database or WAL")
	}
	assertEncryptedFiles(t, destinationPath)
	destinationBeforeVerification, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlaintextMatchesEncrypted(context.Background(), sourcePath, destinationPath, opener); err != nil {
		t.Fatalf("verify copied destination: %v", err)
	}
	destinationAfterVerification, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	mainAfterVerification, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	walAfterVerification, err := os.ReadFile(sourcePath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destinationBeforeVerification, destinationAfterVerification) ||
		!bytes.Equal(mainBefore, mainAfterVerification) ||
		!bytes.Equal(walBefore, walAfterVerification) {
		t.Fatal("verification modified an input database tuple")
	}

	destination, err := opener.Open(context.Background(), destinationPath, Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := opener.CheckIntegrity(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	var sequence, rowCount, userVersion, applicationID int
	if err := destination.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = 'automatic'").Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if err := destination.QueryRow("SELECT count(*) FROM composite").Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if err := destination.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := destination.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	if sequence != 99 || rowCount != 2 || userVersion != 23 || applicationID != 1330466121 {
		t.Fatalf("logical copy differs: sequence=%d rows=%d user_version=%d application_id=%d", sequence, rowCount, userVersion, applicationID)
	}

	if err := CopyPlaintextToEncrypted(context.Background(), sourcePath, destinationPath, opener); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second copy error = %v, want os.ErrExist", err)
	}

	differentPath := filepath.Join(dir, "different.db")
	differentPlaceholder, err := os.OpenFile(differentPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := differentPlaceholder.Close(); err != nil {
		t.Fatal(err)
	}
	different, err := opener.Open(context.Background(), differentPath, Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := different.Exec("CREATE TABLE different (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := different.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlaintextMatchesEncrypted(context.Background(), sourcePath, differentPath, opener); err == nil {
		t.Fatal("verification accepted a logically different encrypted destination")
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
