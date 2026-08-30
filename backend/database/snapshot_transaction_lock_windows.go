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
type snapshotTransactionLock struct {
	file         *os.File
	root         *os.Root
	originalPath string
	originalInfo os.FileInfo
}

func acquireSnapshotTransactionLock(ctx context.Context, snapshotDir string) (*snapshotTransactionLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path := filepath.Join(snapshotDir, snapshotTransactionLockName)
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	if err := prepareSnapshotTransactionLockFile(path, pointer); err != nil &&
		!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, err
	}
	for {
		handle, openErr := windows.CreateFile(
			pointer,
			windows.GENERIC_READ|windows.READ_CONTROL,
			0,
			nil,
			windows.OPEN_EXISTING,
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
			if err := validateSnapshotHandle(file); err != nil {
				_ = file.Close()
				return nil, err
			}
			directoryInfo, err := os.Lstat(snapshotDir)
			if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
				_ = file.Close()
				return nil, errors.Join(errors.New("snapshot transaction directory is unsafe"), err)
			}
			root, err := os.OpenRoot(snapshotDir)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			rootInfo, err := root.Stat(".")
			if err != nil || !os.SameFile(directoryInfo, rootInfo) {
				_ = root.Close()
				_ = file.Close()
				return nil, errors.Join(errors.New("snapshot transaction root changed while acquiring lock"), err)
			}
			return &snapshotTransactionLock{file: file, root: root, originalPath: snapshotDir, originalInfo: directoryInfo}, nil
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

func prepareSnapshotTransactionLockFile(path string, pointer *uint16) error {
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("wrap snapshot transaction lock preparation handle")
	}
	defer file.Close()
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		return err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("snapshot transaction lock is a directory or reparse point")
	}
	if err := fileprivacy.Harden(file); err != nil {
		return err
	}
	return validateSnapshotHandle(file)
}

func (lock *snapshotTransactionLock) verify() error {
	if lock == nil || lock.file == nil || lock.root == nil || lock.originalInfo == nil {
		return errors.New("snapshot transaction lock is unavailable")
	}
	pathInfo, err := os.Lstat(lock.originalPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(lock.originalInfo, pathInfo) {
		return errors.Join(errors.New("snapshot transaction directory pathname changed"), err)
	}
	rootInfo, err := lock.root.Stat(".")
	if err != nil || !os.SameFile(lock.originalInfo, rootInfo) {
		return errors.Join(errors.New("snapshot transaction root changed"), err)
	}
	return nil
}

func (lock *snapshotTransactionLock) release() {
	if lock != nil && lock.file != nil {
		if lock.root != nil {
			_ = lock.root.Close()
		}
		_ = lock.file.Close()
	}
}

func (lock *snapshotTransactionLock) sync() error { return lock.verify() }

func (lock *snapshotTransactionLock) publishManifestNoReplace(replacement, target string) error {
	if err := lock.verify(); err != nil {
		return err
	}
	return publishSnapshotPruneManifestNoReplace(replacement, target)
}

func (lock *snapshotTransactionLock) publishSnapshot(replacement, target string) error {
	if err := lock.verify(); err != nil {
		return err
	}
	return publishSnapshotFile(replacement, target)
}

func (lock *snapshotTransactionLock) replaceManifest(replacement, target string) error {
	if err := lock.verify(); err != nil {
		return err
	}
	return replaceManifestFile(replacement, target)
}
