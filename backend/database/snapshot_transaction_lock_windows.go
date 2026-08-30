//go:build windows

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"omni_money/backend/fileprivacy"
)

// Windows share-mode exclusion provides the same crash-released ownership as
// flock: while this handle is alive no second process can open the transaction
// lock, and the kernel releases ownership when the process exits.
func acquireSnapshotTransactionLock(ctx context.Context, snapshotDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path := filepath.Join(snapshotDir, snapshotTransactionLockName)
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for {
		handle, openErr := windows.CreateFile(
			pointer,
			windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
			0,
			nil,
			windows.OPEN_ALWAYS,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if openErr == nil {
			file := os.NewFile(uintptr(handle), path)
			if file == nil {
				_ = windows.CloseHandle(handle)
				return nil, errors.New("wrap snapshot transaction lock handle")
			}
			var attributes struct {
				Attributes uint32
				ReparseTag uint32
			}
			if err := windows.GetFileInformationByHandleEx(
				handle,
				windows.FileAttributeTagInfo,
				(*byte)(unsafe.Pointer(&attributes)),
				uint32(unsafe.Sizeof(attributes)),
			); err != nil {
				_ = file.Close()
				return nil, err
			}
			if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
				_ = file.Close()
				return nil, errors.New("snapshot transaction lock is a directory or reparse point")
			}
			if err := fileprivacy.Harden(file); err != nil {
				_ = file.Close()
				return nil, err
			}
			if err := validateSnapshotHandle(file); err != nil {
				_ = file.Close()
				return nil, err
			}
			return func() { _ = file.Close() }, nil
		}
		if !errors.Is(openErr, windows.ERROR_SHARING_VIOLATION) && !errors.Is(openErr, windows.ERROR_LOCK_VIOLATION) {
			return nil, openErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
