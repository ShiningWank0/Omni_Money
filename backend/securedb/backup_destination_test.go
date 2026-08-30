package securedb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssertBackupDestinationRejectsSymlinkToPlaceholder(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "placeholder.db")
	if err := os.WriteFile(original, []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "destination.db")
	if err := os.Symlink(original, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := assertBackupDestination(symlink, info); err == nil {
		t.Fatal("symlink to the placeholder passed no-follow destination validation")
	}
}
