//go:build !windows

package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestWriteUniquePrivateFilePermissionsAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	first, err := writeUniquePrivateFile(dir, "transactions.csv", []byte("first"))
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := writeUniquePrivateFile(dir, "transactions.csv", []byte("second"))
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if first == second {
		t.Fatal("existing backup was overwritten")
	}
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(path), got)
		}
	}
	firstData, _ := os.ReadFile(first)
	if string(firstData) != "first" {
		t.Fatalf("first backup changed: %q", firstData)
	}
}

func TestWriteUniquePrivateFileDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "transactions.csv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	created, err := writeUniquePrivateFile(dir, "transactions.csv", []byte("backup"))
	if err != nil {
		t.Fatal(err)
	}
	if created == link {
		t.Fatal("symlink path was used")
	}
	targetData, _ := os.ReadFile(target)
	if string(targetData) != "secret" {
		t.Fatalf("symlink target was overwritten: %q", targetData)
	}
}

func TestBackupToCSVDirectoryUsesSelectedDirectory(t *testing.T) {
	instance, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := NewService(instance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "selected-destination", Date: "2026-08-28", Item: "boundary", Type: "income", Amount: 100,
	}); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), " selected destination ")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	resolvedDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	path, err := service.BackupToCSVDirectory(destination)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != resolvedDestination {
		t.Fatalf("CSV directory = %q, want selected directory", filepath.Dir(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("CSV mode = %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}) || !bytes.Contains(content, []byte("selected-destination")) {
		t.Fatal("CSV did not contain the expected BOM and selected-vault row")
	}
}

func TestOpenVerifiedDirectoryRootRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	link := filepath.Join(parent, "redirected")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	root, err := openVerifiedDirectoryRoot(link)
	if root != nil {
		_ = root.Close()
	}
	if err == nil {
		t.Fatal("symlinked CSV destination was accepted by the pinned root boundary")
	}
}

func TestBackupToCSVDirectoryRejectsInvalidDestination(t *testing.T) {
	service := &Service{}
	if _, err := service.BackupToCSVDirectory(""); err == nil {
		t.Fatal("empty CSV destination was accepted")
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BackupToCSVDirectory(file); err == nil {
		t.Fatal("regular file was accepted as a CSV destination directory")
	}
	if _, err := service.BackupToCSVDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing CSV destination was accepted")
	}
}
