//go:build !windows

package database

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/securedb"
)

func TestSQLCipherLiveWALAndSnapshotStayEncryptedAndPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-data")
	path := filepath.Join(root, "ledger.db")
	key := databaseTestKey(0x39)
	defer key.Destroy()
	instance, err := OpenEncryptedInstance(path, key)
	if shouldSkipSQLCipherTest(err) {
		t.Skipf("SQLCipher integration build is not active: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	db := instance.DB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	const marker = "issue-62-live-wal-financial-canary"
	value := strings.Repeat(marker, 256)
	if _, err := db.Exec("INSERT INTO settings(key, value) VALUES('wal-marker', ?)", value); err != nil {
		t.Fatal(err)
	}
	// Inspect while the writer remains open. Closing it first could remove
	// the WAL and let a test of only the main database pass accidentally.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		artifact := path + suffix
		content, err := os.ReadFile(artifact)
		if err != nil {
			t.Fatalf("required live artifact %s: %v", suffix, err)
		}
		if suffix == "-wal" && len(content) <= 32 {
			t.Fatal("fixture did not leave committed pages in the live WAL")
		}
		if bytes.Contains(content, []byte(marker)) || bytes.HasPrefix(content, []byte("SQLite format 3")) {
			t.Fatalf("plaintext found in live artifact %s", suffix)
		}
		assertMode(t, artifact, 0600)
	}
	assertMode(t, root, 0700)
	snapshotPath, err := instance.CreateSnapshot("")
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Dir(snapshotPath), 0700)
	assertMode(t, snapshotPath, 0600)
	assertDatabaseCiphertext(t, snapshotPath, marker)
	opener := securedb.NewEncryptedOpener(key)
	defer opener.Destroy()
	snapshot, err := opener.Open(context.Background(), snapshotPath, securedb.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := opener.CheckIntegrity(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := snapshot.QueryRow("SELECT value FROM settings WHERE key='wal-marker'").Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != value {
		t.Fatal("snapshot lost committed data from the live WAL")
	}
}

func TestDatabaseAndSnapshotUsePrivatePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-data")
	dbPath := filepath.Join(root, "ledger.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()

	assertMode(t, root, 0700)
	assertMode(t, dbPath, 0600)

	if _, err := GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2026-01-01','item','income',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); err == nil {
			assertMode(t, path, 0600)
		}
	}

	snapshotDir := filepath.Join(root, "snapshots-test")
	snapshotPath, err := CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, snapshotDir, 0700)
	assertMode(t, snapshotPath, 0600)
}

func TestInitDBRejectsSymlinkDatabasePath(t *testing.T) {
	CloseDB()
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(target, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "ledger.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(link); err == nil {
		t.Fatal("InitDB accepted a symlink database path")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "do not overwrite" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestCopyFileDoesNotOverwriteSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	dst := filepath.Join(root, "destination")
	if err := os.WriteFile(src, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Fatal("copyFile overwrote a symlink path")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "target" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
