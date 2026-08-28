package securedb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func createPlainSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE sample (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func TestCopyPlaintextToEncryptedRejectsExistingDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source := createPlainSQLite(t, sourcePath)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(dir, "destination.db")
	original := []byte("must remain unchanged")
	if err := os.WriteFile(destinationPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	opener := NewEncryptedOpener(fixedKey(0x71))
	defer opener.Destroy()

	err := CopyPlaintextToEncrypted(context.Background(), sourcePath, destinationPath, opener)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want os.ErrExist", err)
	}
	content, readErr := os.ReadFile(destinationPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != string(original) {
		t.Fatal("existing migration destination was changed")
	}
}

func TestCopyPlaintextToEncryptedRejectsSymlinkAndNonregularSource(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.db")
	real := createPlainSQLite(t, realPath)
	if err := real.Close(); err != nil {
		t.Fatal(err)
	}
	opener := NewEncryptedOpener(fixedKey(0x72))
	defer opener.Destroy()

	symlinkPath := filepath.Join(dir, "source-link.db")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CopyPlaintextToEncrypted(context.Background(), symlinkPath, filepath.Join(dir, "from-link.db"), opener); err == nil {
		t.Fatal("symlink source was accepted")
	}
	if err := CopyPlaintextToEncrypted(context.Background(), dir, filepath.Join(dir, "from-directory.db"), opener); err == nil {
		t.Fatal("directory source was accepted")
	}
}

func TestCopyPlaintextToEncryptedRejectsSymlinkDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source := createPlainSQLite(t, sourcePath)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	realDestinationDir := filepath.Join(dir, "dest")
	if err := os.Mkdir(realDestinationDir, 0700); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(dir, "dest-link")
	if err := os.Symlink(realDestinationDir, symlinkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	opener := NewEncryptedOpener(fixedKey(0x73))
	defer opener.Destroy()

	if err := CopyPlaintextToEncrypted(context.Background(), sourcePath, filepath.Join(symlinkDir, "target.db"), opener); err == nil {
		t.Fatal("symlink destination directory was accepted")
	}
}

func TestCopyPlaintextToEncryptedRejectsForeignKeyViolationBeforeCreatingOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "invalid.db")
	db, err := sql.Open("sqlite3", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		PRAGMA foreign_keys = OFF;
		CREATE TABLE parent (id INTEGER PRIMARY KEY);
		CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id));
		INSERT INTO child(id, parent_id) VALUES (1, 999);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(dir, "must-not-exist.db")
	opener := NewEncryptedOpener(fixedKey(0x74))
	defer opener.Destroy()

	err = CopyPlaintextToEncrypted(context.Background(), sourcePath, destinationPath, opener)
	if !errors.Is(err, ErrSQLiteIntegrity) {
		t.Fatalf("error = %v, want ErrSQLiteIntegrity", err)
	}
	if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed migration created destination: %v", statErr)
	}
}

func TestMigrationStageRecoveryIsDeterministicAndRejectsUnknownArtifacts(t *testing.T) {
	destinationDir := t.TempDir()
	destinationPath := filepath.Join(destinationDir, "vault.db")
	firstStage, firstMarker, firstRoot, err := prepareMigrationStage(destinationDir, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstRoot.Close(); err != nil {
		t.Fatal(err)
	}
	secondStage, secondMarker, secondRoot, err := prepareMigrationStage(destinationDir, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstStage != secondStage || string(firstMarker) != string(secondMarker) {
		t.Fatal("migration staging identity is not deterministic")
	}
	if err := secondRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondStage, "unexpected"), []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareMigrationStage(destinationDir, destinationPath); err == nil {
		t.Fatal("unknown staging artifact was silently removed")
	}
	if _, err := os.Stat(filepath.Join(secondStage, "unexpected")); err != nil {
		t.Fatalf("unknown staging artifact was changed: %v", err)
	}
}

func TestMigrationStageRecoveryRemovesSoleIncompleteMarker(t *testing.T) {
	destinationDir := t.TempDir()
	destinationPath := filepath.Join(destinationDir, "vault.db")
	stage, marker, root, err := prepareMigrationStage(destinationDir, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	for _, incomplete := range [][]byte{nil, marker[:len(marker)/2]} {
		if err := os.WriteFile(filepath.Join(stage, migrationStageMarkerName), incomplete, 0600); err != nil {
			t.Fatal(err)
		}
		resumedStage, resumedMarker, resumedRoot, err := prepareMigrationStage(destinationDir, destinationPath)
		if err != nil {
			t.Fatalf("resume incomplete marker of length %d: %v", len(incomplete), err)
		}
		if resumedStage != stage || string(resumedMarker) != string(marker) {
			t.Fatal("resumed migration stage identity changed")
		}
		if err := resumedRoot.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogicalDatabaseFingerprintHandlesWithoutRowIDAndInternalSequence(t *testing.T) {
	dir := t.TempDir()
	left := createFingerprintFixture(t, filepath.Join(dir, "left.db"), false)
	defer left.Close()
	right := createFingerprintFixture(t, filepath.Join(dir, "right.db"), true)
	defer right.Close()

	leftFingerprint, err := logicalDatabaseFingerprint(context.Background(), left)
	if err != nil {
		t.Fatal(err)
	}
	rightFingerprint, err := logicalDatabaseFingerprint(context.Background(), right)
	if err != nil {
		t.Fatal(err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatal("row insertion order changed the logical fingerprint")
	}
	if _, err := right.Exec("UPDATE sqlite_sequence SET seq = seq + 1 WHERE name = 'automatic'"); err != nil {
		t.Fatal(err)
	}
	rightFingerprint, err = logicalDatabaseFingerprint(context.Background(), right)
	if err != nil {
		t.Fatal(err)
	}
	if leftFingerprint == rightFingerprint {
		t.Fatal("sqlite_sequence change was not detected")
	}
}

func createFingerprintFixture(t *testing.T, path string, reverse bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE automatic (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT);
		CREATE TABLE composite (left_key TEXT, right_key INTEGER, payload BLOB, PRIMARY KEY(left_key, right_key)) WITHOUT ROWID;
	`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		"INSERT INTO automatic(id, value) VALUES (1, 'one')",
		"INSERT INTO automatic(id, value) VALUES (2, 'two')",
		"INSERT INTO composite(left_key, right_key, payload) VALUES ('a', 2, x'0001')",
		"INSERT INTO composite(left_key, right_key, payload) VALUES ('b', 1, x'0100')",
	}
	if reverse {
		for left, right := 0, len(statements)-1; left < right; left, right = left+1, right-1 {
			statements[left], statements[right] = statements[right], statements[left]
		}
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE sqlite_sequence SET seq = 50 WHERE name = 'automatic'"); err != nil {
		t.Fatal(err)
	}
	return db
}
