package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
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
		".omni-money-snapshot-prune-manifest-crashed.tmp",
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
		".omni-money-snapshot-prune-manifest-crashed.tmp",
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
	staleManifest := filepath.Join(snapshotDir, ".omni-money-snapshot-prune-manifest-crashed.tmp")
	if err := os.WriteFile(staleManifest, []byte("partial"), 0600); err != nil {
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
	if _, err := os.Stat(staleManifest); !os.IsNotExist(err) {
		t.Fatalf("startup left stale prune manifest temporary: %v", err)
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
	firstLock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireSnapshotTransactionLock(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		firstLock.release()
		t.Fatalf("second transaction lock error=%v, want deadline", err)
	}
	firstLock.release()
	secondLock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatalf("transaction lock was not released: %v", err)
	}
	secondLock.release()
}

func TestSnapshotTransactionLockProcessHelper(t *testing.T) {
	if os.Getenv("OMNI_TEST_SNAPSHOT_LOCK_HELPER") != "1" {
		return
	}
	dir := os.Getenv("OMNI_TEST_SNAPSHOT_LOCK_DIR")
	ready := os.Getenv("OMNI_TEST_SNAPSHOT_LOCK_READY")
	lock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	continuePath := os.Getenv("OMNI_TEST_SNAPSHOT_LOCK_CONTINUE")
	resultPath := os.Getenv("OMNI_TEST_SNAPSHOT_LOCK_RESULT")
	if continuePath != "" {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(continuePath); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		boundFile, _, err := lock.createTemporary(".omni-money-boundary-probe-", ".tmp")
		if err != nil {
			t.Fatal(err)
		}
		if err := boundFile.Close(); err != nil {
			t.Fatal(err)
		}
		result := "accepted"
		if err := lock.verify(); err != nil {
			result = "rejected"
		}
		if err := os.WriteFile(resultPath, []byte(result), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	time.Sleep(time.Hour)
}

func TestSnapshotTransactionLockSurvivesMarkerReplacementAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory flock regression")
	}
	dir := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestSnapshotTransactionLockProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		"OMNI_TEST_SNAPSHOT_LOCK_HELPER=1",
		"OMNI_TEST_SNAPSHOT_LOCK_DIR="+dir,
		"OMNI_TEST_SNAPSHOT_LOCK_READY="+ready,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := command.Process
	waited := false
	defer func() {
		if !waited {
			_ = process.Kill()
			_ = command.Wait()
		}
	}()
	waitForSnapshotTestSignal(t, ready)

	marker := filepath.Join(dir, snapshotTransactionLockName)
	if err := os.Rename(marker, marker+".renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(marker); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if lock, err := acquireSnapshotTransactionLock(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			lock.release()
		}
		t.Fatalf("replacement marker created a second lock domain: %v", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	waited = true

	lock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatalf("directory lock was not released after process death: %v", err)
	}
	lock.release()
}

func TestSnapshotTransactionRejectsDirectorySubstitutionAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory substitution regression")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "snapshots")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	control := t.TempDir()
	ready := filepath.Join(control, "ready")
	continuePath := filepath.Join(control, "continue")
	resultPath := filepath.Join(control, "result")
	command := exec.Command(os.Args[0], "-test.run=^TestSnapshotTransactionLockProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		"OMNI_TEST_SNAPSHOT_LOCK_HELPER=1",
		"OMNI_TEST_SNAPSHOT_LOCK_DIR="+dir,
		"OMNI_TEST_SNAPSHOT_LOCK_READY="+ready,
		"OMNI_TEST_SNAPSHOT_LOCK_CONTINUE="+continuePath,
		"OMNI_TEST_SNAPSHOT_LOCK_RESULT="+resultPath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	waitForSnapshotTestSignal(t, ready)
	moved := filepath.Join(parent, "snapshots-moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(continuePath, []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForSnapshotTestSignal(t, resultPath)
	if err := command.Wait(); err != nil {
		t.Fatalf("substitution helper failed: %v", err)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "rejected" {
		t.Fatalf("directory substitution result=%q, want rejected", result)
	}
	replacementEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range replacementEntries {
		if strings.HasPrefix(entry.Name(), ".omni-money-boundary-probe-") {
			t.Fatalf("substituted D2 received root-bound transaction write: %s", entry.Name())
		}
	}
	movedEntries, err := os.ReadDir(moved)
	if err != nil {
		t.Fatal(err)
	}
	foundBoundWrite := false
	for _, entry := range movedEntries {
		foundBoundWrite = foundBoundWrite || strings.HasPrefix(entry.Name(), ".omni-money-boundary-probe-")
	}
	if !foundBoundWrite {
		t.Fatal("locked D1 inode did not receive the root-relative transaction write")
	}
}

func waitForSnapshotTestSignal(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not signal readiness")
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

func TestSnapshotPruneManifestSizeIsValidatedBeforeQuarantine(t *testing.T) {
	dir := t.TempDir()
	const victims = 160
	originals := make([]string, 0, victims)
	for index := 0; index < victims; index++ {
		name := fmt.Sprintf("omni_money_%03d_%s.db", index, strings.Repeat("x", 145))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte{byte(index)}, 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, path)
	}
	planned, err := planSnapshotQuarantineContext(context.Background(), dir, 1, 10_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != victims {
		t.Fatalf("planned victims=%d, want %d", len(planned), victims)
	}
	if _, err := newSnapshotPruneManifest(dir, "omni_money_20260101_000000_000000001.db", strings.Repeat("a", 64), planned); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized manifest error=%v", err)
	}
	for _, path := range originals {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("public victim changed before oversized manifest rejection: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), snapshotPruneArtifactPrefix) {
			t.Fatalf("quarantine artifact %q appeared before manifest size validation", entry.Name())
		}
	}
}

func TestCreateSnapshotOversizedPruneManifestLeavesPublicSetUnchanged(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	const generations = 160
	originals := make(map[string]bool, generations)
	for index := 0; index < generations; index++ {
		name := fmt.Sprintf("omni_money_%03d_%s.db", index, strings.Repeat("z", 170))
		path := filepath.Join(snapshotDir, name)
		if err := os.WriteFile(path, []byte{byte(index)}, 0600); err != nil {
			t.Fatal(err)
		}
		if err := hardenPrivateFile(path); err != nil {
			t.Fatal(err)
		}
		originals[name] = true
	}
	if _, err := instance.CreateSnapshot(snapshotDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized create error=%v", err)
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	publicCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".db") && !strings.HasPrefix(entry.Name(), ".") {
			publicCount++
			if !originals[entry.Name()] {
				t.Fatalf("oversized transaction published unexpected snapshot %q", entry.Name())
			}
		}
		if strings.HasPrefix(entry.Name(), snapshotPruneArtifactPrefix) || entry.Name() == snapshotPruneManifestName {
			t.Fatalf("oversized transaction left prune state %q", entry.Name())
		}
	}
	if publicCount != generations {
		t.Fatalf("public generation count=%d, want %d", publicCount, generations)
	}
}

func TestSnapshotPruneManifestEncodedSizeBoundary(t *testing.T) {
	dir := t.TempDir()
	var entries []snapshotQuarantineEntry
	var lastGood snapshotPruneManifest
	boundaryFound := false
	for index := 0; index < maxSnapshotDirectoryEntries; index++ {
		original := fmt.Sprintf("omni_money_%03d_%s.db", index, strings.Repeat("b", 145))
		quarantined := snapshotPruneArtifactPrefix + fmt.Sprintf("%032x", index+1) + "-" + original
		entries = append(entries, snapshotQuarantineEntry{
			original: filepath.Join(dir, original), quarantined: filepath.Join(dir, quarantined), digest: strings.Repeat("c", 64),
		})
		manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260101_000000_000000001.db", strings.Repeat("a", 64), entries)
		if err != nil {
			if !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("unexpected boundary error at %d victims: %v", len(entries), err)
			}
			boundaryFound = true
			break
		}
		lastGood = manifest
	}
	if !boundaryFound || len(lastGood.Victims) == 0 {
		t.Fatal("manifest byte boundary was not exercised")
	}
	encoded, err := json.Marshal(lastGood)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded))+1 > maxSnapshotPruneManifestBytes {
		t.Fatalf("accepted manifest size=%d exceeds reader limit=%d", len(encoded)+1, maxSnapshotPruneManifestBytes)
	}
}

func TestSnapshotPruneManifestCreateFailureRemovesExactJournal(t *testing.T) {
	for _, step := range []string{"harden", "write", "sync", "close", "publish"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			original := "omni_money_20260101_000000_000000001.db"
			quarantined := snapshotPruneArtifactPrefix + strings.Repeat("1", 32) + "-" + original
			manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260102_000000_000000001.db", strings.Repeat("a", 64), []snapshotQuarantineEntry{{
				original: filepath.Join(dir, original), quarantined: filepath.Join(dir, quarantined), digest: strings.Repeat("b", 64),
			}})
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + step + " failure")
			err = createSnapshotPruneManifestWithCheckpoint(dir, manifest, func(name string) error {
				if name == step {
					return injected
				}
				return nil
			})
			if !errors.Is(err, injected) {
				t.Fatalf("create error=%v, want injected failure", err)
			}
			if _, err := os.Lstat(snapshotPruneManifestPath(dir)); !os.IsNotExist(err) {
				t.Fatalf("failed initial manifest remains: %v", err)
			}
			if err := createSnapshotPruneManifest(dir, manifest); err != nil {
				t.Fatalf("next manifest creation did not recover: %v", err)
			}
		})
	}
}

func TestSnapshotPruneManifestCrashHelper(t *testing.T) {
	checkpoint := os.Getenv("OMNI_TEST_PRUNE_CRASH_CHECKPOINT")
	if checkpoint == "" {
		return
	}
	dir := os.Getenv("OMNI_TEST_PRUNE_CRASH_DIR")
	ready := os.Getenv("OMNI_TEST_PRUNE_CRASH_READY")
	victimDigest := os.Getenv("OMNI_TEST_PRUNE_CRASH_VICTIM_DIGEST")
	original := "omni_money_20260101_000000_000000001.db"
	quarantined := snapshotPruneArtifactPrefix + strings.Repeat("1", 32) + "-" + original
	manifest, err := newSnapshotPruneManifest(dir, "omni_money_20260102_000000_000000001.db", strings.Repeat("a", 64), []snapshotQuarantineEntry{{
		original: filepath.Join(dir, original), quarantined: filepath.Join(dir, quarantined), digest: victimDigest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = createSnapshotPruneManifestWithCheckpoint(dir, manifest, func(name string) error {
		if name == checkpoint {
			if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
				return err
			}
			time.Sleep(time.Hour)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPruneManifestForcedKillPublicationBoundary(t *testing.T) {
	for _, checkpoint := range []string{"publish", "published"} {
		t.Run(checkpoint, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(t.TempDir(), "ready")
			original := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
			quarantined := filepath.Join(dir, snapshotPruneArtifactPrefix+strings.Repeat("1", 32)+"-"+filepath.Base(original))
			if err := os.WriteFile(quarantined, []byte("victim"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := hardenPrivateFile(quarantined); err != nil {
				t.Fatal(err)
			}
			victimDigest, err := digestDatabaseFile(quarantined)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestSnapshotPruneManifestCrashHelper$", "-test.count=1")
			command.Env = append(os.Environ(),
				"OMNI_TEST_PRUNE_CRASH_CHECKPOINT="+checkpoint,
				"OMNI_TEST_PRUNE_CRASH_DIR="+dir,
				"OMNI_TEST_PRUNE_CRASH_READY="+ready,
				"OMNI_TEST_PRUNE_CRASH_VICTIM_DIGEST="+victimDigest,
			)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waitForSnapshotTestSignal(t, ready)
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()

			manifest, found, err := readSnapshotPruneManifest(dir)
			if checkpoint == "publish" {
				if err != nil || found {
					t.Fatalf("pre-publication death exposed fixed journal: found=%t err=%v", found, err)
				}
				if err := cleanupSnapshotPruneManifestTemps(context.Background(), dir); err != nil {
					t.Fatalf("recover pre-publication orphan: %v", err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if isSnapshotPruneManifestTempName(entry.Name()) {
						t.Fatalf("orphan journal temporary remains: %s", entry.Name())
					}
				}
				return
			}
			if err != nil || !found {
				t.Fatalf("post-publication death left unreadable journal: found=%t err=%v", found, err)
			}
			if manifest.Phase != "prepared" || len(manifest.Victims) != 1 {
				t.Fatalf("post-publication journal is incomplete: %+v", manifest)
			}
			if err := recoverSnapshotPruneTransactionAtStart(context.Background(), dir); err != nil {
				t.Fatalf("recover complete fixed journal after process death: %v", err)
			}
			if content, err := os.ReadFile(original); err != nil || string(content) != "victim" {
				t.Fatalf("recovery did not roll victim back: content=%q err=%v", content, err)
			}
			if _, err := os.Lstat(quarantined); !os.IsNotExist(err) {
				t.Fatalf("recovery left quarantined victim: %v", err)
			}
			if _, err := os.Lstat(snapshotPruneManifestPath(dir)); !os.IsNotExist(err) {
				t.Fatalf("recovery left fixed journal: %v", err)
			}
		})
	}
}

func TestSnapshotRetentionOverflowIsFailClosed(t *testing.T) {
	if !snapshotRetentionExceedsBudget(math.MaxInt64-4096, 8192, math.MaxInt64) {
		t.Fatal("overflowing retention addition was accepted")
	}
	if snapshotRetentionExceedsBudget(math.MaxInt64-8192, 8192, math.MaxInt64) {
		t.Fatal("exact retention boundary was rejected")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "omni_money_20260101_000000_000000001.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(math.MaxInt64 - 4096); err != nil {
		_ = file.Close()
		t.Skipf("filesystem does not support huge sparse files: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivateFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := planSnapshotQuarantineContext(context.Background(), dir, 30, math.MaxInt64, 8192); err == nil {
		t.Fatal("huge sparse snapshot bypassed retention and validation limits")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed planning mutated public snapshot set: %v", err)
	}
}

func TestSnapshotPruneManifestTempCleanupAndTamperBoundary(t *testing.T) {
	t.Run("valid orphan", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".omni-money-snapshot-prune-manifest-orphan.tmp")
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := cleanupSnapshotPruneManifestTemps(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("valid orphan remains: %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("do not remove"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ".omni-money-snapshot-prune-manifest-link.tmp")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := cleanupSnapshotPruneManifestTemps(context.Background(), dir); err == nil {
			t.Fatal("cleanup followed a symlinked manifest temporary")
		}
		if content, err := os.ReadFile(target); err != nil || string(content) != "do not remove" {
			t.Fatalf("symlink target changed: content=%q err=%v", content, err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".omni-money-snapshot-prune-manifest-hardlink.tmp")
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, filepath.Join(dir, "second-link")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		if err := cleanupSnapshotPruneManifestTemps(context.Background(), dir); err == nil {
			t.Fatal("cleanup accepted a multiply-linked manifest temporary")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("hard-linked temporary was removed: %v", err)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix mode bits are not authoritative on Windows")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, ".omni-money-snapshot-prune-manifest-readable.tmp")
		if err := os.WriteFile(path, []byte("partial"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if err := cleanupSnapshotPruneManifestTemps(context.Background(), dir); err == nil {
			t.Fatal("cleanup accepted a non-private manifest temporary")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("non-private temporary was removed: %v", err)
		}
	})
}

func TestCreateSnapshotRecoversManifestTempEntryCapExhaustion(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	for index := 0; index < maxSnapshotDirectoryEntries+32; index++ {
		name := fmt.Sprintf(".omni-money-snapshot-prune-manifest-%03d.tmp", index)
		if err := os.WriteFile(filepath.Join(snapshotDir, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := instance.CreateSnapshot(snapshotDir); err != nil {
		t.Fatalf("create did not recover manifest temp entry exhaustion: %v", err)
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".omni-money-snapshot-prune-manifest-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("orphan manifest temporary remains: %s", entry.Name())
		}
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
