package database

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"omni_money/backend/fileprivacy"
)

func firstSnapshotTransactionLock(locks []*snapshotTransactionLock) *snapshotTransactionLock {
	if len(locks) == 0 {
		return nil
	}
	return locks[0]
}

func snapshotTransactionLstat(lock *snapshotTransactionLock, path string) (os.FileInfo, error) {
	if lock != nil {
		return lock.lstat(path)
	}
	return os.Lstat(path)
}

func validateSnapshotTransactionArtifact(lock *snapshotTransactionLock, path string) (os.FileInfo, error) {
	if lock == nil {
		return validatePrivateArtifact(path)
	}
	file, err := lock.openArtifact(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

func snapshotTransactionRename(lock *snapshotTransactionLock, oldPath, newPath string) error {
	if lock != nil {
		return lock.rename(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func assertSnapshotTransactionArtifact(lock *snapshotTransactionLock, file *os.File, path string) error {
	if lock == nil {
		return assertOpenFileAtPath(file, path)
	}
	if file == nil {
		return errors.New("snapshot transaction artifact descriptor is nil")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := lock.lstat(path)
	if err != nil {
		return err
	}
	if !sameSnapshotInfo(fileInfo, pathInfo) {
		return errors.New("snapshot transaction artifact pathname changed")
	}
	return nil
}

func syncSnapshotTransactionDirectory(lock *snapshotTransactionLock, path string) error {
	if lock != nil {
		return lock.sync()
	}
	return syncDirectory(path)
}

func syncSnapshotTransactionFile(lock *snapshotTransactionLock, file *os.File, path string) error {
	if file == nil {
		return errors.New("snapshot transaction file is nil")
	}
	if err := assertSnapshotTransactionArtifact(lock, file, path); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return syncSnapshotTransactionDirectory(lock, filepath.Dir(path))
}

func removeSnapshotTransactionSQLiteFiles(lock *snapshotTransactionLock, path string) error {
	if lock == nil {
		return removeSQLiteFiles(path)
	}
	var errs []error
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := lock.removeArtifact(candidate); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// directName restricts transaction operations to direct children of the
// identity-bound snapshot root. Snapshot artifacts and their SQLite sidecars
// are deliberately flat, so nested paths are never required here.
func (lock *snapshotTransactionLock) directName(path string) (string, error) {
	if lock == nil || lock.root == nil {
		return "", errors.New("snapshot transaction root is unavailable")
	}
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(lock.originalPath) {
		return "", errors.New("snapshot transaction path escaped its locked directory")
	}
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "", errors.New("snapshot transaction child name is invalid")
	}
	return name, nil
}

func (lock *snapshotTransactionLock) lstat(path string) (os.FileInfo, error) {
	name, err := lock.directName(path)
	if err != nil {
		return nil, err
	}
	return lock.root.Lstat(name)
}

func (lock *snapshotTransactionLock) readDir(ctx context.Context, maxEntries int) ([]os.DirEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if lock == nil || lock.root == nil || maxEntries <= 0 {
		return nil, errors.New("snapshot transaction directory read boundary is invalid")
	}
	directory, err := lock.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries := make([]os.DirEntry, 0, minInt(maxEntries, snapshotDirectoryReadBatchSize))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := maxEntries - len(entries)
		batchSize := snapshotDirectoryReadBatchSize
		if remaining < batchSize {
			batchSize = remaining
		}
		batch, readErr := directory.ReadDir(batchSize)
		entries = append(entries, batch...)
		if len(entries) > maxEntries {
			return nil, errors.New("directory entry limit exceeded")
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return entries, nil
			}
			return nil, readErr
		}
		if len(entries) == maxEntries {
			extra, extraErr := directory.ReadDir(1)
			if len(extra) != 0 {
				return nil, errors.New("directory entry limit exceeded")
			}
			if extraErr != nil && !errors.Is(extraErr, io.EOF) {
				return nil, extraErr
			}
			return entries, nil
		}
	}
}

func (lock *snapshotTransactionLock) openArtifact(path string) (*os.File, error) {
	name, err := lock.directName(path)
	if err != nil {
		return nil, err
	}
	pathInfo, err := lock.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("snapshot transaction artifact is a symlink")
	}
	file, err := lock.root.Open(name)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !sameSnapshotInfo(pathInfo, info) || !validSnapshotFile(info) {
		return fail(errors.New("snapshot transaction artifact identity is unsafe"))
	}
	if err := fileprivacy.ValidatePrivateFile(file); err != nil {
		return fail(err)
	}
	return file, nil
}

func (lock *snapshotTransactionLock) createPlaceholder(path string) (*os.File, error) {
	name, err := lock.directName(path)
	if err != nil {
		return nil, err
	}
	file, err := lock.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		_ = lock.root.Remove(name)
		return nil, err
	}
	if err := fileprivacy.Harden(file); err != nil {
		return fail(err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	pathInfo, err := lock.root.Lstat(name)
	if err != nil || !sameSnapshotInfo(fileInfo, pathInfo) {
		return fail(errors.Join(errors.New("snapshot placeholder changed during creation"), err))
	}
	return file, nil
}

func (lock *snapshotTransactionLock) createTemporary(prefix, suffix string) (*os.File, string, error) {
	if lock == nil || lock.root == nil || prefix == "" || suffix == "" {
		return nil, "", errors.New("snapshot transaction temporary boundary is invalid")
	}
	for attempt := 0; attempt < 16; attempt++ {
		random := make([]byte, 16)
		if _, err := cryptorand.Read(random); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(random) + suffix
		clear(random)
		file, err := lock.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return file, filepath.Join(lock.originalPath, name), nil
	}
	return nil, "", errors.New("snapshot transaction temporary name allocation failed")
}

func (lock *snapshotTransactionLock) rename(oldPath, newPath string) error {
	oldName, err := lock.directName(oldPath)
	if err != nil {
		return err
	}
	newName, err := lock.directName(newPath)
	if err != nil {
		return err
	}
	return lock.root.Rename(oldName, newName)
}

func (lock *snapshotTransactionLock) removeArtifact(path string) error {
	name, err := lock.directName(path)
	if err != nil {
		return err
	}
	file, err := lock.openArtifact(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := lock.root.Lstat(name)
	if err != nil || !sameSnapshotInfo(fileInfo, pathInfo) {
		return errors.Join(errors.New("snapshot transaction artifact changed before removal"), err)
	}
	return lock.root.Remove(name)
}
