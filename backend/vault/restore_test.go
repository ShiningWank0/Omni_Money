package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRootRestoreOperationDrainsChildrenAndClosesInstance(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-restore", testVaultID, testKey(0x37))
	if err != nil {
		t.Fatal(err)
	}
	instance := leaseInstance(root)
	if _, err := leaseDB(root).Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2026-08-30','before','income',1,1)`); err != nil {
		t.Fatal(err)
	}
	snapshotPath, err := root.CreateSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaseDB(root).Exec(`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2026-08-30','after','income',2,3)`); err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	op, err := root.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire("user-restore", testVaultID, testKey(0x37)); !errors.Is(err, ErrDraining) {
		t.Fatalf("acquire during restore = %v, want ErrDraining", err)
	}
	root.Release()

	done := make(chan error, 1)
	go func() { done <- op.RestoreSnapshot(context.Background(), filepath.Base(snapshotPath)) }()
	select {
	case err := <-done:
		t.Fatalf("restore completed while child was in flight: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	child.Release()
	if err := <-done; err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The operation's exactly-once release closes the instance and zeroizes its
	// opener once all request/root references have drained.
	if instance.DB() != nil {
		t.Fatal("restore operation left the old instance open")
	}
	reopened, err := manager.Acquire("user-restore", testVaultID, testKey(0x37))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := leaseDB(reopened).QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restored transaction count = %d, want 1", count)
	}
	reopened.Release()
	// RestoreOperation.Release is exactly-once even after RestoreSnapshot has
	// already released it on every success/error path.
	op.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreOperationDuplicateAndCanceledWaitCloseFailClosed(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-restore-cancel", testVaultID, testKey(0x38))
	if err != nil {
		t.Fatal(err)
	}
	instance := leaseInstance(root)
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	op, err := root.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.BeginRestore(); !errors.Is(err, ErrRestoreInFlight) {
		t.Fatalf("duplicate restore error = %v, want ErrRestoreInFlight", err)
	}
	root.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := op.RestoreSnapshot(ctx, "missing.db"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore error = %v, want context.Canceled", err)
	}
	// The canceled operation has released its internal reference but leaves the
	// existing child live until it is explicitly released.
	if instance.DB() == nil {
		t.Fatal("canceled restore closed an in-flight child")
	}
	child.Release()
	deadline := time.Now().Add(2 * time.Second)
	for instance.DB() != nil {
		if time.Now().After(deadline) {
			t.Fatal("canceled restore did not close after child release")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreOperationWaitsForManagerShutdownAndReleasesExactlyOnce(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-restore-shutdown", testVaultID, testKey(0x39))
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	op, err := root.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	root.Release()
	result := make(chan error, 1)
	go func() { result <- op.RestoreSnapshot(context.Background(), "missing.db") }()
	select {
	case err := <-result:
		t.Fatalf("restore completed before child release: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := manager.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("manager shutdown error = %v, want context deadline", err)
	}
	child.Release()
	if err := <-result; err == nil {
		t.Fatal("missing snapshot restore unexpectedly succeeded")
	}
	// The operation's deferred Release and the child's Release race with the
	// shutdown path safely; a final close completes without exposing a vault.
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreOperationClosesAfterPostCloseFilesystemFailure(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-restore-failure", testVaultID, testKey(0x3a))
	if err != nil {
		t.Fatal(err)
	}
	instance := leaseInstance(root)
	snapshotPath, err := root.CreateSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(manager.root, testVaultID, "ledger.db-journal")
	if err := os.Mkdir(journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalPath, "held"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := root.BeginRestore()
	if err != nil {
		t.Fatal(err)
	}
	root.Release()
	if err := op.RestoreSnapshot(context.Background(), filepath.Base(snapshotPath)); err == nil {
		t.Fatal("post-close filesystem failure was ignored")
	}
	deadline := time.Now().Add(2 * time.Second)
	for instance.DB() != nil {
		if time.Now().After(deadline) {
			t.Fatal("failed restore was republished instead of closing the drained instance")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.RemoveAll(journalPath); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
