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
// boundary across processes sharing one snapshot directory. flock ownership
// is tied to this descriptor, so a process crash releases the lock without
// deleting or trusting a stale PID marker.
func acquireSnapshotTransactionLock(ctx context.Context, snapshotDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path := filepath.Join(snapshotDir, snapshotTransactionLockName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600) // #nosec G304 -- fixed lock path inside a validated private directory.
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) {
		_ = file.Close()
		return nil, err
	}
	if err := fileprivacy.Harden(file); err != nil {
		return fail(err)
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !validSnapshotFile(info) {
		return fail(errors.New("snapshot transaction lock is not a private regular file"))
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
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
}
