//go:build windows

package publicassets

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openNoFollow(root string, parts []string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	directory, err := windows.CreateFile(name, windows.FILE_GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(directory) }()
	if err := validateHandle(directory, true); err != nil {
		return nil, err
	}
	for index, part := range parts {
		component, err := windows.NewNTUnicodeString(part)
		if err != nil {
			return nil, err
		}
		attributes := windows.OBJECT_ATTRIBUTES{
			Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: directory,
			ObjectName: component, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		}
		isDirectory := index < len(parts)-1
		options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if isDirectory {
			options |= windows.FILE_DIRECTORY_FILE
		} else {
			options |= windows.FILE_NON_DIRECTORY_FILE
		}
		var next windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&next, windows.FILE_GENERIC_READ, &attributes, &status, nil, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_OPEN, options, 0, 0)
		if err != nil {
			return nil, err
		}
		if err := validateHandle(next, isDirectory); err != nil {
			_ = windows.CloseHandle(next)
			return nil, err
		}
		if isDirectory {
			_ = windows.CloseHandle(directory)
			directory = next
			continue
		}
		file := os.NewFile(uintptr(next), strings.Join(parts, "/"))
		if file == nil {
			_ = windows.CloseHandle(next)
			return nil, os.ErrInvalid
		}
		return file, nil
	}
	return nil, os.ErrInvalid
}

func validateHandle(handle windows.Handle, directory bool) error {
	var info struct{ Attributes, ReparseTag uint32 }
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	if info.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return os.ErrNotExist
	}
	return nil
}
