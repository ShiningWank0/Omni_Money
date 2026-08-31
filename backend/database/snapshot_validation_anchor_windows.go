//go:build windows

package database

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openSnapshotValidationAnchor denies write and delete sharing while SQLite
// validates candidatePath. Windows therefore guarantees that the pathname
// cannot be exchanged or modified until the validation connection closes.
// The post-open root comparison binds this pathname open back to the pinned
// transaction root and rejects a race before CreateFile completed.
func openSnapshotValidationAnchor(lock *snapshotTransactionLock, path string) (*os.File, string, error) {
	name, err := lock.directName(path)
	if err != nil {
		return nil, "", err
	}
	rootInfo, err := lock.root.Lstat(name)
	if err != nil {
		return nil, "", err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, "", err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, "", err
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, "", err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, "", errors.New("snapshot validation candidate is a directory or reparse point")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, "", errors.New("wrap snapshot validation candidate handle")
	}
	fail := func(err error) (*os.File, string, error) {
		_ = file.Close()
		return nil, "", err
	}
	if err := validateSnapshotHandle(file); err != nil {
		return fail(err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	postRootInfo, err := lock.root.Lstat(name)
	if err != nil || !sameSnapshotInfo(rootInfo, fileInfo) || !sameSnapshotInfo(fileInfo, postRootInfo) {
		return fail(errors.Join(errors.New("snapshot validation candidate changed while anchoring"), err))
	}
	return file, path, nil
}
