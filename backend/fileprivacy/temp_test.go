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
