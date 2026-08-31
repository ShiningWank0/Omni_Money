//go:build !windows

package database

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"omni_money/backend/fileprivacy"
)

// acquireSnapshotTransactionLock serializes the retention/publication
// boundary across processes sharing one snapshot directory. The advisory lock
// is held on the stable directory inode rather than on the visible marker:
// renaming or replacing the marker therefore cannot create a second lock
// domain. A post-flock identity check rejects a concurrent directory swap.
type snapshotTransactionLock struct {
	directory    *os.File
	root         *os.Root
	originalPath string
	originalInfo os.FileInfo
}

func acquireSnapshotTransactionLock(ctx context.Context, snapshotDir string) (*snapshotTransactionLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	directory, err := os.Open(snapshotDir) // #nosec G304 -- caller-validated exact snapshot directory.
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*snapshotTransactionLock, error) {
		_ = directory.Close()
		return nil, err
	}
	for {
		err = syscall.Flock(int(directory.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fail(err)
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	directoryInfo, err := directory.Stat()
	if err != nil {
		return fail(err)
	}
	if err := fileprivacy.ValidatePrivateDirectory(directory); err != nil {
		return fail(errors.Join(errors.New("snapshot transaction directory is not private"), err))
	}
	pathInfo, err := os.Lstat(snapshotDir)
	if err != nil {
		return fail(err)
	}
	if !directoryInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(directoryInfo, pathInfo) {
		return fail(errors.New("snapshot transaction directory changed while acquiring lock"))
	}
	root, err := os.OpenRoot(snapshotDir)
	if err != nil {
		return fail(err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, rootInfo) {
		_ = root.Close()
		return fail(errors.Join(errors.New("snapshot transaction root changed while acquiring lock"), err))
	}
	rootDirectory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return fail(err)
	}
	rootPrivacyErr := fileprivacy.ValidatePrivateDirectory(rootDirectory)
	rootCloseErr := rootDirectory.Close()
	if rootPrivacyErr != nil || rootCloseErr != nil {
		_ = root.Close()
		return fail(errors.Join(errors.New("snapshot transaction root is not private"), rootPrivacyErr, rootCloseErr))
	}
	failRoot := func(err error) (*snapshotTransactionLock, error) {
		_ = root.Close()
		return fail(err)
	}
	marker, err := root.OpenFile(snapshotTransactionLockName, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return failRoot(err)
	}
	if err := fileprivacy.Harden(marker); err != nil {
		_ = marker.Close()
		return failRoot(err)
	}
	info, err := marker.Stat()
	if err != nil || !validSnapshotFile(info) {
		_ = marker.Close()
		if err != nil {
			return failRoot(err)
		}
		return failRoot(errors.New("snapshot transaction lock is not a private regular file"))
	}
	markerInfo, rootMarkerErr := root.Lstat(snapshotTransactionLockName)
	if rootMarkerErr != nil || !sameSnapshotInfo(info, markerInfo) {
		_ = marker.Close()
		return failRoot(errors.Join(errors.New("snapshot transaction marker changed while acquiring lock"), rootMarkerErr))
	}
	if err := marker.Close(); err != nil {
		return failRoot(err)
	}
	return &snapshotTransactionLock{directory: directory, root: root, originalPath: snapshotDir, originalInfo: directoryInfo}, nil
}

func (lock *snapshotTransactionLock) verify() error {
	if lock == nil || lock.directory == nil || lock.root == nil || lock.originalInfo == nil {
		return errors.New("snapshot transaction lock is unavailable")
	}
	heldInfo, err := lock.directory.Stat()
	if err != nil || !os.SameFile(lock.originalInfo, heldInfo) {
		return errors.Join(errors.New("snapshot transaction directory handle changed"), err)
	}
	if err := fileprivacy.ValidatePrivateDirectory(lock.directory); err != nil {
		return errors.Join(errors.New("snapshot transaction directory privacy changed"), err)
	}
	rootInfo, err := lock.root.Stat(".")
	if err != nil || !os.SameFile(lock.originalInfo, rootInfo) {
		return errors.Join(errors.New("snapshot transaction root changed"), err)
	}
	rootDirectory, err := lock.root.Open(".")
	if err != nil {
		return errors.Join(errors.New("snapshot transaction root privacy unavailable"), err)
	}
	rootPrivacyErr := fileprivacy.ValidatePrivateDirectory(rootDirectory)
	rootCloseErr := rootDirectory.Close()
	if rootPrivacyErr != nil || rootCloseErr != nil {
		return errors.Join(errors.New("snapshot transaction root privacy changed"), rootPrivacyErr, rootCloseErr)
	}
	pathInfo, err := os.Lstat(lock.originalPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(lock.originalInfo, pathInfo) {
		return errors.Join(errors.New("snapshot transaction directory pathname changed"), err)
	}
	return nil
}

func (lock *snapshotTransactionLock) release() {
	if lock == nil {
		return
	}
	if lock.root != nil {
		_ = lock.root.Close()
	}
	if lock.directory != nil {
		_ = syscall.Flock(int(lock.directory.Fd()), syscall.LOCK_UN)
		_ = lock.directory.Close()
	}
}

func (lock *snapshotTransactionLock) sync() error {
	if lock == nil || lock.root == nil {
		return errors.New("snapshot transaction root is unavailable")
	}
	directory, err := lock.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (lock *snapshotTransactionLock) publishSnapshot(replacement, target string) error {
	return lock.rename(replacement, target)
}

func (lock *snapshotTransactionLock) replaceManifest(replacement, target string) error {
	return lock.rename(replacement, target)
}
