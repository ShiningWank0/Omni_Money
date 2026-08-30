//go:build !windows

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"omni_money/backend/fileprivacy"
)

// acquireSnapshotTransactionLock serializes the retention/publication
// boundary across processes sharing one snapshot directory. The advisory lock
// is held on the stable directory inode rather than on the visible marker:
// renaming or replacing the marker therefore cannot create a second lock
// domain. A post-flock identity check rejects a concurrent directory swap.
func acquireSnapshotTransactionLock(ctx context.Context, snapshotDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	directory, err := os.Open(snapshotDir) // #nosec G304 -- caller-validated exact snapshot directory.
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) {
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
	pathInfo, err := os.Lstat(snapshotDir)
	if err != nil {
		return fail(err)
	}
	if !directoryInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(directoryInfo, pathInfo) {
		return fail(errors.New("snapshot transaction directory changed while acquiring lock"))
	}
	path := filepath.Join(snapshotDir, snapshotTransactionLockName)
	marker, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600) // #nosec G304 -- exact coordination marker inside the identity-bound directory.
	if err != nil {
		return fail(err)
	}
	if err := fileprivacy.Harden(marker); err != nil {
		_ = marker.Close()
		return fail(err)
	}
	info, err := marker.Stat()
	if err != nil || !validSnapshotFile(info) {
		_ = marker.Close()
		if err != nil {
			return fail(err)
		}
		return fail(errors.New("snapshot transaction lock is not a private regular file"))
	}
	if err := assertOpenFileAtPath(marker, path); err != nil {
		_ = marker.Close()
		return fail(err)
	}
	if err := marker.Close(); err != nil {
		return fail(err)
	}
	return func() {
		_ = syscall.Flock(int(directory.Fd()), syscall.LOCK_UN)
		_ = directory.Close()
	}, nil
}
