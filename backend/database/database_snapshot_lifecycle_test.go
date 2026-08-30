package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSnapshotQuarantineRollbackAndStartupRecovery(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	for _, path := range []string{oldPath, filepath.Join(dir, "omni_money_20260102_000000_000000001.db")} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), dir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantined) != 1 {
		t.Fatalf("quarantine entries=%d, want 1", len(quarantined))
	}
	if _, err := os.Stat(quarantined[0].quarantined); err != nil {
		t.Fatalf("quarantined file missing: %v", err)
	}
	if err := rollbackSnapshotQuarantine(quarantined, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(quarantined[0].original); err != nil {
		t.Fatalf("rollback did not restore original: %v", err)
	}

	quarantined, err = quarantineSnapshotsContext(context.Background(), dir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupSnapshotPruneArtifacts(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(quarantined[0].original); err != nil {
		t.Fatalf("startup cleanup did not restore quarantined snapshot: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), snapshotPruneArtifactPrefix) {
			t.Fatalf("quarantine artifact remains after recovery: %s", entry.Name())
		}
	}
}

func TestSnapshotPruneJournalDoesNotResurrectPublishedVictims(t *testing.T) {
	dir := t.TempDir()
	victimPath := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	otherPath := filepath.Join(dir, "omni_money_20260102_000000_000000001.db")
	for _, path := range []string{victimPath, otherPath} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), dir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	targetName := "omni_money_20260103_000000_000000001.db"
	targetPath := filepath.Join(dir, targetName)
	if err := os.WriteFile(targetPath, make([]byte, 40), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(targetPath); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDatabaseFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(dir, targetName, digest, quarantined)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Phase = "published"
	if err := writeSnapshotPruneManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSnapshotPruneArtifacts(context.Background(), dir); err != nil {
		t.Fatalf("published prune recovery failed: %v", err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("published target disappeared: %v", err)
	}
	if _, err := os.Stat(victimPath); !os.IsNotExist(err) {
		t.Fatalf("published victim was resurrected: %v", err)
	}
	if _, err := os.Stat(snapshotPruneManifestPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("prune manifest remains after finalize: %v", err)
	}
}

func TestSnapshotPruneJournalRollsBackPreparedQuarantine(t *testing.T) {
	dir := t.TempDir()
	victimPath := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	otherPath := filepath.Join(dir, "omni_money_20260102_000000_000000001.db")
	for _, path := range []string{victimPath, otherPath} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), dir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260103_000000_000000001.db", strings.Repeat("a", 64), quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshotPruneManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSnapshotPruneArtifacts(context.Background(), dir); err != nil {
		t.Fatalf("prepared prune recovery failed: %v", err)
	}
	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("prepared victim was not restored: %v", err)
	}
	if _, err := os.Stat(snapshotPruneManifestPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("prepared prune manifest remains: %v", err)
	}
}

func TestCreateSnapshotRecoversPreparedPruneJournalBeforeNewTransaction(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	victimPath := filepath.Join(snapshotDir, "omni_money_20260101_000000_000000001.db")
	otherPath := filepath.Join(snapshotDir, "omni_money_20260102_000000_000000001.db")
	for _, path := range []string{victimPath, otherPath} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), snapshotDir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(snapshotDir, "omni_money_20260103_000000_000000001.db", strings.Repeat("a", 64), quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if err := createSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := instance.CreateSnapshot(snapshotDir); err != nil {
		t.Fatalf("new snapshot did not recover prior prepared transaction: %v", err)
	}
	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("prior victim was not restored before the new transaction: %v", err)
	}
	if _, err := os.Stat(snapshotPruneManifestPath(snapshotDir)); !os.IsNotExist(err) {
		t.Fatalf("resolved prune journal remains: %v", err)
	}
}

func TestCreateSnapshotFailsClosedOnUnresolvedPruneJournal(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	victimPath := filepath.Join(snapshotDir, "omni_money_20260101_000000_000000001.db")
	otherPath := filepath.Join(snapshotDir, "omni_money_20260102_000000_000000001.db")
	for _, path := range []string{victimPath, otherPath} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), snapshotDir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(snapshotDir, "omni_money_20260103_000000_000000001.db", strings.Repeat("a", 64), quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if err := createSnapshotPruneManifest(snapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantined[0].quarantined, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := instance.CreateSnapshot(snapshotDir); err == nil {
		t.Fatal("new snapshot overwrote unresolved tampered prune state")
	}
	if _, err := os.Stat(snapshotPruneManifestPath(snapshotDir)); err != nil {
		t.Fatalf("unresolved prune journal was removed: %v", err)
	}
}

func TestCreateSnapshotPruneManifestIsExclusiveAcrossConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	quarantined := filepath.Join(dir, snapshotPruneArtifactPrefix+strings.Repeat("1", 32)+"-"+filepath.Base(original))
	if err := os.WriteFile(quarantined, []byte("victim"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(quarantined); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDatabaseFile(quarantined)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260102_000000_000000001.db", strings.Repeat("a", 64), []snapshotQuarantineEntry{{
		original: original, quarantined: quarantined, digest: digest,
	}})
	if err != nil {
		t.Fatal(err)
	}

	const writers = 8
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- createSnapshotPruneManifest(dir, manifest)
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("exclusive manifest writers succeeded=%d, want 1", succeeded)
	}
	readBack, found, err := readSnapshotPruneManifest(dir)
	if err != nil || !found {
		t.Fatalf("read exclusive manifest: found=%t err=%v", found, err)
	}
	if readBack.NewDigest != manifest.NewDigest || readBack.Victims[0].Digest != digest {
		t.Fatal("exclusive manifest contents were replaced")
	}
}

func TestSnapshotTransactionLockSerializesConcurrentOwners(t *testing.T) {
	dir := t.TempDir()
	firstRelease, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireSnapshotTransactionLock(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		firstRelease()
		t.Fatalf("second transaction lock error=%v, want deadline", err)
	}
	firstRelease()
	secondRelease, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatalf("transaction lock was not released: %v", err)
	}
	secondRelease()
}

func TestPreparedPruneRollbackStateMatrixIsCrashIdempotent(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	quarantined := filepath.Join(dir, snapshotPruneArtifactPrefix+strings.Repeat("2", 32)+"-"+filepath.Base(original))
	contents := []byte("victim-bytes")
	if err := os.WriteFile(original, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(original); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDatabaseFile(original)
	if err != nil {
		t.Fatal(err)
	}
	entry := snapshotQuarantineEntry{original: original, quarantined: quarantined, digest: digest}

	// Crash after rename-back: original exists, quarantine is absent.
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err != nil {
		t.Fatalf("already-restored rollback was not idempotent: %v", err)
	}
	if err := os.Rename(original, quarantined); err != nil {
		t.Fatal(err)
	}
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err != nil {
		t.Fatalf("quarantined rollback failed: %v", err)
	}

	// Both names mean ambiguity; neither name means loss. Both fail closed.
	if err := os.WriteFile(quarantined, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(quarantined); err != nil {
		t.Fatal(err)
	}
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err == nil {
		t.Fatal("rollback accepted both original and quarantine")
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(quarantined); err != nil {
		t.Fatal(err)
	}
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err == nil {
		t.Fatal("rollback accepted missing original and quarantine")
	}
}

func TestPublishedPruneFinalizeStateMatrixIsCrashIdempotent(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	quarantined := filepath.Join(dir, snapshotPruneArtifactPrefix+strings.Repeat("3", 32)+"-"+filepath.Base(original))
	if err := os.WriteFile(quarantined, []byte("victim"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(quarantined); err != nil {
		t.Fatal(err)
	}
	digest, err := digestDatabaseFile(quarantined)
	if err != nil {
		t.Fatal(err)
	}
	entry := snapshotQuarantineEntry{original: original, quarantined: quarantined, digest: digest}
	if err := finalizeSnapshotQuarantine(context.Background(), []snapshotQuarantineEntry{entry}, dir); err != nil {
		t.Fatalf("first finalize failed: %v", err)
	}
	if err := finalizeSnapshotQuarantine(context.Background(), []snapshotQuarantineEntry{entry}, dir); err != nil {
		t.Fatalf("finalize retry after deletion was not idempotent: %v", err)
	}
	if err := os.WriteFile(original, []byte("victim"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(original); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSnapshotQuarantine(context.Background(), []snapshotQuarantineEntry{entry}, dir); err == nil {
		t.Fatal("published finalize accepted a reappeared original")
	}
}

func TestPreparedPruneRollbackAppliesSameStateMatrixToSidecars(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	quarantined := filepath.Join(dir, snapshotPruneArtifactPrefix+strings.Repeat("4", 32)+"-"+filepath.Base(original))
	sidecarOriginal := original + "-wal"
	sidecarQuarantined := quarantined + "-wal"
	for _, path := range []string{original, sidecarOriginal} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := digestDatabaseFile(original)
	if err != nil {
		t.Fatal(err)
	}
	sidecarDigest, err := digestDatabaseFile(sidecarOriginal)
	if err != nil {
		t.Fatal(err)
	}
	entry := snapshotQuarantineEntry{original: original, quarantined: quarantined, digest: digest}
	entry.sidecars[0] = snapshotQuarantineSidecar{
		original: sidecarOriginal, quarantined: sidecarQuarantined, digest: sidecarDigest, present: true,
	}
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err != nil {
		t.Fatalf("already-restored database and sidecar were not idempotent: %v", err)
	}
	if err := os.Remove(sidecarOriginal); err != nil {
		t.Fatal(err)
	}
	if err := rollbackSnapshotQuarantine([]snapshotQuarantineEntry{entry}, dir); err == nil {
		t.Fatal("rollback accepted a sidecar missing from both names")
	}
}

func TestPublishedPruneRecoveryRequiresRecordedTarget(t *testing.T) {
	dir := t.TempDir()
	victimPath := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	otherPath := filepath.Join(dir, "omni_money_20260102_000000_000000001.db")
	for _, path := range []string{victimPath, otherPath} {
		if err := os.WriteFile(path, make([]byte, 40), 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
	}
	quarantined, err := quarantineSnapshotsContext(context.Background(), dir, 2, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260103_000000_000000001.db", strings.Repeat("a", 64), quarantined)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Phase = "published"
	if err := writeSnapshotPruneManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := recoverSnapshotPruneManifest(context.Background(), dir); err == nil {
		t.Fatal("published recovery deleted victims without its recorded target")
	}
	if _, err := os.Stat(quarantined[0].quarantined); err != nil {
		t.Fatalf("victim was removed after missing-target recovery: %v", err)
	}
	if _, err := os.Stat(snapshotPruneManifestPath(dir)); err != nil {
		t.Fatalf("journal was removed after missing-target recovery: %v", err)
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
