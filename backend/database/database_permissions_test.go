//go:build !windows

package database

import (
	"os"
	"path/filepath"
	"testing"
)

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
