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
