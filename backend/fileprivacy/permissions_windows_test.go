//go:build windows

package fileprivacy

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCreateExclusivePrivateFileDACL(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := CreateExclusive(root, directory, "private.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !IsPrivate(file, info) {
		t.Fatal("CreateExclusive file failed the actual DACL privacy check")
	}

	if _, err := CreateExclusive(root, directory, "private.csv"); !os.IsExist(err) {
		t.Fatalf("duplicate private file error = %v, want already exists", err)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("private file DACL still inherits permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatal("private file DACL is absent")
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{currentUser.User.Sid.String(): false}
	if currentUser.User.Sid.String() != system.String() {
		want[system.String()] = false
	}
	if int(dacl.AceCount) != len(want) {
		t.Fatalf("private file DACL ACE count = %d, want %d distinct principals", dacl.AceCount, len(want))
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("private file DACL ACE %d is not allow-only", i)
		}
		if ace.Mask != windows.ACCESS_MASK(fileAllAccess) {
			t.Fatalf("private file DACL ACE %d mask = %#x, want full file access", i, ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			t.Fatalf("private file DACL unexpectedly grants %s", sid.String())
		}
		want[sid.String()] = true
	}
	for sid, present := range want {
		if !present {
			t.Fatalf("private file DACL does not grant required principal %s", sid)
		}
	}
}
