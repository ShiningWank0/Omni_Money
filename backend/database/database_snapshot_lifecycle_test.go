package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
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

func TestRestoreLifecycleAdmissionDoesNotDeadlockWithAutoSnapshot(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}

	// Hold the only process-wide validation slot so the already-running auto
	// worker is forced to wait. Restore must drain first, allowing the worker to
	// observe closing and exit instead of waiting on a gate held by restore.
	snapshotValidationAdmission <- struct{}{}
	instance.StartAutoSnapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- instance.RestoreSnapshotContext(ctx, snapshotDir, filepath.Base(snapshotPath))
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("restore error = %v, want admission deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restore remained blocked behind auto-snapshot admission")
	}
	snapshotValidationAdmissionRelease()
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotStagingArtifactsAreNotPublicSnapshots(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		".omni-money-snapshot-staging-crashed.db",
		".omni-money-list-validation-crashed.db",
	} {
		if err := os.WriteFile(filepath.Join(snapshotDir, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := instance.ListSnapshots(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != filepath.Base(snapshotPath) {
		t.Fatalf("listed snapshots = %v, want only %s", entries, filepath.Base(snapshotPath))
	}
	for _, name := range []string{
		".omni-money-snapshot-staging-crashed.db",
		".omni-money-list-validation-crashed.db",
	} {
		if _, err := os.Stat(filepath.Join(snapshotDir, name)); !os.IsNotExist(err) {
			t.Fatalf("stale staging artifact %s remains: %v", name, err)
		}
	}
}

func TestStartupRemovesStaleSnapshotStagingArtifacts(t *testing.T) {
	instance, databasePath, snapshotDir := newPlainSnapshotTestInstance(t)
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(snapshotDir, ".omni-money-list-validation-crashed.db")
	if err := os.WriteFile(stale, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPlainInstance(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("startup left stale validation artifact: %v", err)
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
