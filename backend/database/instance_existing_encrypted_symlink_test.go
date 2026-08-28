//go:build !windows

package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingEncryptedInstanceRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	if err := os.WriteFile(target, []byte("not a database but long enough"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "vault.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if instance, err := OpenExistingEncryptedInstance(link, strictTestRawKey(0x53)); err == nil {
		_ = instance.Close()
		t.Fatal("symlinked encrypted database path was accepted")
	}
}
