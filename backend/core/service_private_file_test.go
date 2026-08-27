//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"testing"
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
