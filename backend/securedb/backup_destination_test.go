package securedb

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupPlaceholderDescriptorSupportsFinalHeaderVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	placeholder, err := openBackupPlaceholder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer placeholder.Close()

	// SQLite writes through a separate descriptor. Keep this test focused on
	// the retained handle's portable access contract: after bytes appear, the
	// exact placeholder descriptor must support the encrypted-header read.
	header := []byte("encrypted-header")
	if _, err := placeholder.WriteAt(header, 0); err != nil {
		t.Fatal(err)
	}
	if err := placeholder.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := requireEncryptedHeaderFile(placeholder); err != nil {
		t.Fatalf("retained backup placeholder is not readable: %v", err)
	}
	if offset, err := placeholder.Seek(0, io.SeekCurrent); err != nil || offset != int64(len(header)) {
		t.Fatalf("header verification offset = %d, err=%v", offset, err)
	}
}

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
