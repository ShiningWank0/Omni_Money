package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInstancesKeepDatabaseAndSnapshotStateIsolated(t *testing.T) {
	root := t.TempDir()
	first, err := OpenPlainInstance(filepath.Join(root, "first", "vault.db"))
	if err != nil {
		t.Fatalf("open first instance: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenPlainInstance(filepath.Join(root, "second", "vault.db"))
	if err != nil {
		t.Fatalf("open second instance: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if _, err := first.DB().Exec(`INSERT INTO transactions
		(account, date, item, type, amount, balance, memo)
		VALUES ('first', '2026-08-28', 'first-only', 'expense', 100, 0, '')`); err != nil {
		t.Fatalf("insert first transaction: %v", err)
	}
	if _, err := second.DB().Exec(`INSERT INTO transactions
		(account, date, item, type, amount, balance, memo)
		VALUES ('second', '2026-08-28', 'second-only', 'income', 200, 0, '')`); err != nil {
		t.Fatalf("insert second transaction: %v", err)
	}

	assertTransactionItem(t, first, "first-only")
	assertTransactionItem(t, second, "second-only")

	firstSnapshot, err := first.CreateSnapshot("")
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}
	secondSnapshot, err := second.CreateSnapshot("")
	if err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}
	if filepath.Dir(firstSnapshot) == filepath.Dir(secondSnapshot) {
		t.Fatalf("instance snapshots unexpectedly share a directory: %s", filepath.Dir(firstSnapshot))
	}
	firstSnapshots, err := first.ListSnapshots("")
	if err != nil {
		t.Fatalf("list first snapshots: %v", err)
	}
	secondSnapshots, err := second.ListSnapshots("")
	if err != nil {
		t.Fatalf("list second snapshots: %v", err)
	}
	if len(firstSnapshots) != 1 || len(secondSnapshots) != 1 {
		t.Fatalf("unexpected isolated snapshot counts: first=%d second=%d", len(firstSnapshots), len(secondSnapshots))
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first instance: %v", err)
	}
	if first.DB() != nil {
		t.Fatal("first DB remained published after Close")
	}
	assertTransactionItem(t, second, "second-only")
}

func TestInstanceRestoreUsesItsOwnOpenerAndPath(t *testing.T) {
	root := t.TempDir()
	first, err := OpenPlainInstance(filepath.Join(root, "first", "vault.db"))
	if err != nil {
		t.Fatalf("open first instance: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenPlainInstance(filepath.Join(root, "second", "vault.db"))
	if err != nil {
		t.Fatalf("open second instance: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	insertTransactionItem(t, first, "before-snapshot")
	snapshotPath, err := first.CreateSnapshot("")
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	insertTransactionItem(t, first, "after-snapshot")
	insertTransactionItem(t, second, "second-unchanged")

	if err := first.RestoreSnapshot("", filepath.Base(snapshotPath)); err != nil {
		t.Fatalf("restore first instance: %v", err)
	}
	var firstCount int
	if err := first.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&firstCount); err != nil {
		t.Fatalf("count restored transactions: %v", err)
	}
	if firstCount != 1 {
		t.Fatalf("restored first transaction count = %d, want 1", firstCount)
	}
	assertTransactionItem(t, second, "second-unchanged")
}

func TestInstanceAutoSnapshotsRunIndependently(t *testing.T) {
	root := t.TempDir()
	first, err := OpenPlainInstance(filepath.Join(root, "first", "vault.db"))
	if err != nil {
		t.Fatalf("open first instance: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenPlainInstance(filepath.Join(root, "second", "vault.db"))
	if err != nil {
		t.Fatalf("open second instance: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	first.StartAutoSnapshot()
	second.StartAutoSnapshot()
	waitForAutoSnapshot(t, first)
	waitForAutoSnapshot(t, second)

	firstSnapshots, err := first.ListSnapshots("")
	if err != nil {
		t.Fatalf("list first snapshots: %v", err)
	}
	secondSnapshots, err := second.ListSnapshots("")
	if err != nil {
		t.Fatalf("list second snapshots: %v", err)
	}
	if len(firstSnapshots) != 1 || len(secondSnapshots) != 1 {
		t.Fatalf("unexpected auto snapshot counts: first=%d second=%d", len(firstSnapshots), len(secondSnapshots))
	}
}

func insertTransactionItem(t *testing.T, instance *Instance, item string) {
	t.Helper()
	if _, err := instance.DB().Exec(`INSERT INTO transactions
		(account, date, item, type, amount, balance, memo)
		VALUES ('test', '2026-08-28', ?, 'expense', 100, 0, '')`, item); err != nil {
		t.Fatalf("insert transaction %q: %v", item, err)
	}
}

func assertTransactionItem(t *testing.T, instance *Instance, want string) {
	t.Helper()
	var got string
	if err := instance.DB().QueryRow("SELECT item FROM transactions").Scan(&got); err != nil {
		t.Fatalf("read transaction item: %v", err)
	}
	if got != want {
		t.Fatalf("transaction item = %q, want %q", got, want)
	}
}

func waitForAutoSnapshot(t *testing.T, instance *Instance) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		instance.snapshotMu.Lock()
		running := instance.snapshotRunning
		instance.snapshotMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for automatic snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
