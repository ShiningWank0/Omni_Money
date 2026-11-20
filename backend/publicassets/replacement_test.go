package publicassets

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func renameAssetForTest(ctx context.Context, oldpath, newpath string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return retryAssetRename(ctx, func() error { return os.Rename(oldpath, newpath) }, retryableAssetRenameError)
}

func retryAssetRename(ctx context.Context, rename func() error, retryable func(error) bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := rename()
		if err == nil || !retryable(err) {
			return err
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func TestAssetRenameRetryRequiresSuccessfulReplacement(t *testing.T) {
	temporary := errors.New("temporary sharing conflict")
	calls := 0
	err := retryAssetRename(t.Context(), func() error {
		calls++
		if calls == 1 {
			return temporary
		}
		return nil
	}, func(err error) bool { return errors.Is(err, temporary) })
	if err != nil || calls != 2 {
		t.Fatalf("replacement result = %v after %d attempts, want success after 2", err, calls)
	}
}

func TestAssetRenamePermanentErrorIsNotRetried(t *testing.T) {
	permanent := errors.New("invalid replacement")
	calls := 0
	err := retryAssetRename(t.Context(), func() error { calls++; return permanent }, func(error) bool { return false })
	if !errors.Is(err, permanent) || calls != 1 {
		t.Fatalf("replacement result = %v after %d attempts, want original error after 1", err, calls)
	}
}

func TestAssetRenameCancellationPreservesUnresolvedFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conflict := errors.New("unresolved sharing conflict")
	calls := 0
	err := retryAssetRename(ctx, func() error { calls++; cancel(); return conflict }, func(error) bool { return true })
	if calls != 1 || !errors.Is(err, conflict) || !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement result = %v after %d attempts, want unresolved conflict and cancellation", err, calls)
	}
}

func TestAssetRenameCancelledContextDoesNotMutateFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := retryAssetRename(ctx, func() error { t.Fatal("rename after cancellation"); return nil }, func(error) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement result = %v, want cancellation", err)
	}
}
