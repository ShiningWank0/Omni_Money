//go:build windows

package securedb

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"omni_money/backend/fileprivacy"
)

// assertBackupDestination uses an OPEN_REPARSE_POINT handle so a symlink or
// other reparse point cannot be silently followed by the pathname-based SQL
// opener. The handle identity is then compared with the exclusive placeholder
// created by Backup.
func assertBackupDestination(path string, expected os.FileInfo) error {
	file, err := openNoFollowPrivateFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, actual) {
		return fmt.Errorf("backup destination was replaced during backup")
	}
	return nil
}

func openNoFollowPrivateFile(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap private file handle")
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		_ = file.Close()
		return nil, err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = file.Close()
		return nil, errors.New("private file is a directory or reparse point")
	}
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
