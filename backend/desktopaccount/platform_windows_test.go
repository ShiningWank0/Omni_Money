//go:build windows

package desktopaccount

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsCreatePrivateTempUsesProtectedDACL(t *testing.T) {
	directory := t.TempDir()
	if err := hardenPrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	file, err := createPrivateTemp(directory, ".secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertWindowsProtectedDACL(t, windows.Handle(file.Fd()), false)
}

func TestWindowsPrivateDirectoryUsesProtectedDACL(t *testing.T) {
	root := t.TempDir()
	directories := []string{root, filepath.Join(root, "vault"), filepath.Join(root, "vault", "snapshots")}
	if err := os.MkdirAll(directories[len(directories)-1], 0700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		if err := hardenPrivateDirectory(directory); err != nil {
			t.Fatal(err)
		}
		assertWindowsDirectoryProtectedDACL(t, directory)
	}
}

func assertWindowsDirectoryProtectedDACL(t *testing.T, directory string) {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	assertWindowsProtectedDACL(t, handle, true)
}

func TestWindowsOpenRegularNoFollowRejectsReparsePoint(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation is unavailable: %v", err)
	}
	file, err := openRegularNoFollow(link)
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("openRegularNoFollow accepted a Windows reparse point")
	}
}

func assertWindowsProtectedDACL(t *testing.T, handle windows.Handle, wantInheritance bool) {
	t.Helper()
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("private Windows DACL still inherits permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		t.Fatalf("private Windows DACL ACE count = %v, want current user and LocalSystem only", dacl)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(currentUser.User.Sid) {
		t.Fatalf("private Windows owner = %v, want current account %s", owner, currentUser.User.Sid.String())
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{currentUser.User.Sid.String(): false, system.String(): false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			t.Fatalf("private Windows DACL grants unexpected principal %s", sid.String())
		}
		want[sid.String()] = true
		inheritance := ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != 0
		if inheritance != wantInheritance {
			t.Fatalf("private Windows DACL inheritance = %t, want %t", inheritance, wantInheritance)
		}
	}
	for sid, present := range want {
		if !present {
			t.Fatalf("private Windows DACL does not grant %s", sid)
		}
	}
}
