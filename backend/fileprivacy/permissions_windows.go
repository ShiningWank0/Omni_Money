//go:build windows

// Package fileprivacy applies OS-specific owner-only permissions to files.
package fileprivacy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ALL_ACCESS is the standard file object access mask. x/sys/windows does
// not expose this aggregate constant on all supported versions.
const fileAllAccess uint32 = 0x1F01FF

func privateSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows account: %w", err)
	}
	sddl := "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("create private Windows DACL: %w", err)
	}
	return descriptor, nil
}

// CreateExclusive creates the file with its protected DACL in the same
// CreateFile call. It then proves that the created handle names the file under
// the directory root pinned before sensitive content was generated.
func CreateExclusive(root *os.Root, directory, name string) (*os.File, error) {
	descriptor, err := privateSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private Windows path: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap private Windows file handle")
	}

	createdInfo, createdErr := file.Stat()
	rootInfo, rootErr := root.Lstat(name)
	if createdErr != nil || rootErr != nil || !os.SameFile(createdInfo, rootInfo) {
		cleanupErr := deleteOpenWindowsFile(handle)
		closeErr := file.Close()
		return nil, errors.Join(
			errors.New("created Windows file is outside the pinned destination"),
			createdErr,
			rootErr,
			cleanupErr,
			closeErr,
		)
	}
	return file, nil
}

func deleteOpenWindowsFile(handle windows.Handle) error {
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
	)
}

// Harden replaces inherited permissions before any sensitive bytes are
// written. Windows ignores Unix mode bits, so explicitly allow only the
// current account and LocalSystem and protect the DACL from inheritance.
func Harden(file *os.File) error {
	descriptor, err := privateSecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private Windows DACL: %w", err)
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply private Windows DACL: %w", err)
	}
	return nil
}

// IsPrivate proves the actual DACL on the open handle rather than trusting
// Windows' emulated FileInfo mode bits. Only the current user and LocalSystem
// may have full access, and inheritance must be disabled.
func IsPrivate(file *os.File, info os.FileInfo) bool {
	if file == nil || info == nil || !info.Mode().IsRegular() {
		return false
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 2 {
		return false
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return false
	}
	want := map[string]bool{current.User.Sid.String(): false, system.String(): false}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != windows.ACCESS_MASK(fileAllAccess) {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			return false
		}
		if want[sid.String()] {
			return false
		}
		want[sid.String()] = true
	}
	for _, present := range want {
		if !present {
			return false
		}
	}
	return true
}
