package fileprivacy

import (
	"os"
	"testing"
)

func TestCreatePrivateTempFileCreatesBoundedPrivateArtifact(t *testing.T) {
	temp, err := CreatePrivateTempFile("omni-money-test-")
	if err != nil {
		t.Fatal(err)
	}
	path := temp.Path
	if temp.File == nil || temp.Root == nil || temp.Dir == "" {
		t.Fatal("private temp file did not return file, root, and directory")
	}
	info, err := temp.File.Stat()
	if err != nil {
		_ = temp.Cleanup()
		t.Fatal(err)
	}
	if !IsPrivate(temp.File, info) {
		_ = temp.Cleanup()
		t.Fatal("private temp file failed privacy check")
	}
	if _, err := temp.File.Write([]byte("test")); err != nil {
		_ = temp.Cleanup()
		t.Fatal(err)
	}
	if err := temp.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("private temp path still exists: %v", err)
	}
}

func TestPrivateTempCleanupDoesNotRemoveReplacedFile(t *testing.T) {
	temp, err := CreatePrivateTempFile("omni-money-identity-")
	if err != nil {
		t.Fatal(err)
	}
	path := temp.Path
	replacement := path + ".replacement"
	if err := temp.File.Close(); err != nil {
		t.Fatal(err)
	}
	temp.File = nil
	if err := os.Rename(path, replacement); err != nil {
		t.Fatal(err)
	}
	replacementFile, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.Rename(replacement, path)
		t.Fatal(err)
	}
	_ = replacementFile.Close()
	if err := temp.Cleanup(); err == nil {
		t.Fatal("cleanup accepted a replaced private file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement file was removed: %v", err)
	}
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("original file was not retained for inspection: %v", err)
	}
	_ = os.Remove(path)
	_ = os.Remove(replacement)
	_ = os.Remove(temp.Dir)
}
