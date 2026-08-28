package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPruneSnapshotsEnforcesCountAndByteBudget(t *testing.T) {
	dir := t.TempDir()
	for i, size := range []int{40, 40, 40, 40} {
		name := filepath.Join(dir, []string{
			"omni_money_20260101_000000_000000001.db",
			"omni_money_20260102_000000_000000001.db",
			"omni_money_20260103_000000_000000001.db",
			"omni_money_20260104_000000_000000001.db",
		}[i])
		if err := os.WriteFile(name, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	protected := filepath.Join(dir, "omni_money_20260104_000000_000000001.db")
	if err := pruneSnapshots(dir, 3, 80, protected); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("remaining snapshots=%d, want 2", len(entries))
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected snapshot was removed: %v", err)
	}
	if entries[0].Name() != "omni_money_20260103_000000_000000001.db" {
		t.Fatalf("oldest eligible snapshots were not removed: %v", entries)
	}
}

func TestPruneSnapshotsFailsWhenOnlyProtectedFileExceedsBudget(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "omni_money_20260104_000000_000000001.db")
	if err := os.WriteFile(protected, make([]byte, 81), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneSnapshots(dir, 30, 80, protected); err == nil {
		t.Fatal("oversized protected snapshot was accepted")
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected snapshot was removed on failure: %v", err)
	}
}

func TestSnapshotMaxTotalBytesValidation(t *testing.T) {
	t.Setenv("SNAPSHOT_MAX_TOTAL_BYTES", "8192")
	if got, err := snapshotMaxTotalBytes(); err != nil || got != 8192 {
		t.Fatalf("budget=%d err=%v", got, err)
	}
	t.Setenv("SNAPSHOT_MAX_TOTAL_BYTES", "0")
	if _, err := snapshotMaxTotalBytes(); err == nil {
		t.Fatal("zero budget was accepted")
	}
}
