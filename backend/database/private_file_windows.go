//go:build windows

package database

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"omni_money/backend/fileprivacy"
)

func hardenPrivateFile(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return errors.New("private database path is a directory or reparse point")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("wrap private database handle")
	}
	defer file.Close()
	return fileprivacy.Harden(file)
}
