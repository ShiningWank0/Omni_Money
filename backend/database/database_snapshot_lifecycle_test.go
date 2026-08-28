package database

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAutoSnapshotCloseWaitsAndResetsState exercises the lifecycle boundary
// that is easy to miss when tests use TempDir: AutoSnapshot is asynchronous,
// but closing/reinitializing the database must not let its worker outlive the
// database it copied.
func TestAutoSnapshotCloseWaitsAndResetsState(t *testing.T) {
	firstDir := t.TempDir()
	firstDB := filepath.Join(firstDir, "omni_money.db")
	if err := InitDB(firstDB); err != nil {
		t.Fatalf("InitDB(first) failed: %v", err)
	}
	AutoSnapshot()
	CloseDB()

	snapshotEntries, err := os.ReadDir(filepath.Join(firstDir, "snapshots"))
	if err != nil {
		t.Fatalf("ReadDir(first snapshots) failed: %v", err)
	}
	if len(snapshotEntries) == 0 {
		t.Fatal("CloseDB returned before the scheduled snapshot completed")
	}

	defaultInstance.snapshotMu.Lock()
	running, pending := defaultInstance.snapshotRunning, defaultInstance.snapshotPending
	defaultInstance.snapshotMu.Unlock()
	if running || pending {
		t.Fatalf("snapshot worker still active after CloseDB: running=%t pending=%t", running, pending)
	}

	secondDir := t.TempDir()
	secondDB := filepath.Join(secondDir, "omni_money.db")
	if err := InitDB(secondDB); err != nil {
		t.Fatalf("InitDB(second) failed: %v", err)
	}
	AutoSnapshot()
	CloseDB()

	if _, err := os.Stat(filepath.Join(secondDir, "snapshots")); err != nil {
		t.Fatalf("second database snapshot directory missing: %v", err)
	}
}

func TestConcurrentDatabaseLifecycleTransitionsAreSerialized(t *testing.T) {
	firstDB := filepath.Join(t.TempDir(), "first", "omni_money.db")
	secondDB := filepath.Join(t.TempDir(), "second", "omni_money.db")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := InitDB(firstDB); err != nil {
			t.Errorf("InitDB(first) failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := InitDB(secondDB); err != nil {
			t.Errorf("InitDB(second) failed: %v", err)
		}
	}()
	wg.Wait()

	CloseDB()
	if firstInfo, err := os.Stat(firstDB); err != nil || firstInfo.IsDir() {
		t.Fatalf("first database was not initialized: err=%v", err)
	}
	if secondInfo, err := os.Stat(secondDB); err != nil || secondInfo.IsDir() {
		t.Fatalf("second database was not initialized: err=%v", err)
	}
}

func TestCopyFileRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.db")
	dst := filepath.Join(dir, "destination.db")
	if err := os.WriteFile(src, []byte("new"), 0600); err != nil {
		t.Fatalf("write source failed: %v", err)
	}
	if err := os.WriteFile(dst, []byte("original"), 0600); err != nil {
		t.Fatalf("write destination failed: %v", err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Fatal("copyFile overwrote an existing destination")
	}
	contents, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination failed: %v", err)
	}
	if string(contents) != "original" {
		t.Fatalf("destination changed after failed copy: %q", contents)
	}
}
