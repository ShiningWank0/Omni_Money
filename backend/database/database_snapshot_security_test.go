package database

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"omni_money/backend/fileprivacy"
	"omni_money/backend/securedb"
)

func privateSnapshotTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := fileprivacy.HardenDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newPlainSnapshotTestInstance(t *testing.T) (*Instance, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	snapshotDir := filepath.Join(dir, "snapshots")
	if err := os.Mkdir(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fileprivacy.HardenDirectory(snapshotDir); err != nil {
		t.Fatal(err)
	}
	return instance, path, snapshotDir
}

func insertSnapshotTestTransaction(t *testing.T, instance *Instance, item string) {
	t.Helper()
	if _, err := instance.DB().Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash', ?, ?, 'income', 1, 1)",
		time.Now().UTC().Format("2006-01-02"), item,
	); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSnapshotRejectsCorruptionAndPreservesLiveDatabase(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "before")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	insertSnapshotTestTransaction(t, instance, "after")

	if err := os.WriteFile(snapshotPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(snapshotPath)); err == nil {
		t.Fatal("corrupt snapshot was accepted")
	}
	entries, err := instance.ListSnapshots(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry == filepath.Base(snapshotPath) {
			t.Fatal("corrupt snapshot was exposed by ListSnapshots")
		}
	}
	if instance.DB() == nil {
		t.Fatal("live database was unpublished after a candidate validation failure")
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("live transaction count after corrupt restore = %d, want 2", count)
	}
}

func TestListSnapshotsReportsValidationInfrastructureFailure(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "before-opener-failure")
	if _, err := instance.CreateSnapshot(snapshotDir); err != nil {
		t.Fatal(err)
	}
	instance.opener.Destroy()
	if _, err := instance.ListSnapshots(snapshotDir); !errors.Is(err, securedb.ErrDestroyed) {
		t.Fatalf("ListSnapshots error=%v, want ErrDestroyed", err)
	}
}

func TestRestoreSnapshotPostCloseFilesystemFailureLeavesInstanceUnpublished(t *testing.T) {
	instance, dbPath, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "before")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at a SQLite sidecar pathname makes the explicit sidecar
	// cleanup fail after the live connection has already been closed.
	journalPath := dbPath + "-journal"
	if err := os.Mkdir(journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalPath, "undeletable"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(snapshotPath)); err == nil {
		t.Fatal("restore unexpectedly ignored sidecar cleanup failure")
	}
	if instance.DB() != nil {
		t.Fatal("post-close filesystem failure republished the instance")
	}
}

func TestRestoreSnapshotRejectsTraversalAndForeignPathNames(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "original")
	for _, name := range []string{
		"../ledger.db", "..\\ledger.db", "nested/ledger.db", "ledger", "ledger.sqlite", "",
	} {
		if err := instance.RestoreSnapshot(snapshotDir, name); err == nil {
			t.Fatalf("unsafe snapshot name %q was accepted", name)
		}
	}
	foreignDir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.Mkdir(foreignDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignDir, "foreign.db"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(foreignDir, "foreign.db"); err == nil {
		t.Fatal("snapshot from an unrelated directory was accepted")
	}
}

func TestListAndRestoreRejectSymlinkAndHardlinkSnapshots(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "original")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	validName := filepath.Base(snapshotPath)
	linkName := filepath.Join(snapshotDir, "symlink.db")
	if err := os.Symlink(snapshotPath, linkName); err != nil {
		// Windows CI may not grant symlink creation to the test process. The
		// no-follow implementation is still covered by the Unix test build.
		t.Logf("symlink unavailable: %v", err)
	} else {
		if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(linkName)); err == nil {
			t.Fatal("symlink snapshot was accepted")
		}
		entries, err := instance.ListSnapshots(snapshotDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry == filepath.Base(linkName) {
				t.Fatal("symlink appeared in snapshot listing")
			}
		}
	}

	hardlinkName := filepath.Join(snapshotDir, "hardlink.db")
	if err := os.Link(snapshotPath, hardlinkName); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(hardlinkName)); err == nil {
		t.Fatal("hardlink snapshot was accepted")
	}
	entries, err := instance.ListSnapshots(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry == validName || entry == filepath.Base(hardlinkName) {
			t.Fatalf("hardlinked snapshot appeared in listing: %v", entries)
		}
	}
}

func TestSnapshotSourceIdentityRejectsReplacement(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "original")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := validateSnapshotSource(snapshotPath, snapshotDir, filepath.Base(snapshotPath))
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(snapshotDir, "replacement.db")
	if err := os.Rename(snapshotPath, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := openSnapshotFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	actual, err := opened.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if snapshotSourceMatches(expected, actual) {
		t.Fatal("same-name source replacement was treated as the inspected file")
	}
	// A replaced source is rejected before the live database is touched.
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(snapshotPath)); err == nil {
		t.Fatal("replacement snapshot was accepted")
	}
}

func TestListValidationUsesStableSourceDescriptorAcrossPathSwap(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "descriptor-source")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}

	source, err := openSnapshotFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}
	movedPath := snapshotPath + ".moved"
	if err := os.Rename(snapshotPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte("attacker replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Restore the original pathname after the source path was swapped. The
	// candidate must still contain bytes from the already-open descriptor, not
	// from either pathname lookup.
	defer func() {
		_ = os.Remove(snapshotPath)
		_ = os.Rename(movedPath, snapshotPath)
	}()
	candidatePath, candidate, err := temporaryDatabaseFile(snapshotDir, ".omni-money-list-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = candidate.Close()
		_ = removeSQLiteFiles(candidatePath)
	}()
	if err := copyFileToOpenBounded(source, candidate, maxSnapshotValidationBytes); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if after, err := source.Stat(); err != nil || !snapshotSourceMatches(sourceInfo, after) {
		t.Fatalf("source descriptor changed during copy: %v", err)
	}
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(movedPath, snapshotPath); err != nil {
		t.Fatal(err)
	}
	validated, err := instance.opener.Open(t.Context(), candidatePath, securedb.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.validateSnapshotDatabase(validated, candidatePath); err != nil {
		_ = validated.Close()
		t.Fatalf("descriptor candidate did not validate: %v", err)
	}
	if err := validated.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackRestoreFailureKeepsInstanceClosed(t *testing.T) {
	instance, dbPath, _ := newPlainSnapshotTestInstance(t)
	if _, err := instance.DB().Exec("INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash', '2026-01-01', 'original', 'income', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if err := instance.DB().Close(); err != nil {
		t.Fatal(err)
	}
	instance.mu.Lock()
	instance.db = nil
	instance.mu.Unlock()
	backupPath := filepath.Join(filepath.Dir(dbPath), "restore-backup.db")
	if err := os.Rename(dbPath, backupPath); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(filepath.Dir(dbPath), "restore-candidate.db")
	if err := os.WriteFile(candidatePath, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Force the rollback reopen to fail. The old file is put back, but the
	// instance must remain unpublished so a manager can close it fail-closed.
	instance.opener.Destroy()
	err := rollbackRestoreFiles(dbPath, backupPath, candidatePath, instance)
	if err == nil {
		t.Fatal("rollback unexpectedly reported success after reopen failure")
	}
	if instance.DB() != nil {
		t.Fatal("rollback reopen failure republished an instance")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("rollback did not restore original file: %v", err)
	}
}

func TestRestoreBackupDestinationSupportsAtomicRename(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "ledger.db")
	if err := os.WriteFile(current, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := randomDatabasePath(dir, ".omni-money-restore-backup-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(current, backup); err != nil {
		t.Fatalf("atomic rename to generated backup path failed: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup path missing after rename: %v", err)
	}
}

func TestRestoreCandidateIdentityRejectsPathSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("candidate rename fixture requires POSIX open-file semantics")
	}
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, ".omni-money-restore-candidate-test.db")
	candidate, err := os.OpenFile(candidatePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.WriteString("validated candidate"); err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	if err := candidate.Sync(); err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	validatedInfo, err := candidate.Stat()
	if err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	validatedDigest, err := digestOpenFile(candidate)
	if err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "other-valid-snapshot.db")
	if err := os.WriteFile(replacement, []byte("another valid same-key image"), 0o600); err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	if err := os.Rename(candidatePath, candidatePath+".detached"); err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	if err := os.Rename(replacement, candidatePath); err != nil {
		candidate.Close()
		t.Fatal(err)
	}
	defer candidate.Close()
	if err := assertOpenFileAtPath(candidate, candidatePath); err == nil {
		t.Fatal("candidate path substitution was accepted")
	}
	if err := assertPathDigest(candidatePath, validatedInfo, validatedDigest); err == nil {
		t.Fatal("candidate path digest substitution was accepted")
	}
}

func TestStartupReclaimsDurableRestoreArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{".omni-money-restore-backup-", ".omni-money-restore-candidate-"} {
		artifact := filepath.Join(dir, prefix+"stale.db")
		if err := os.WriteFile(artifact, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".omni-money-restore-") {
			t.Fatalf("stale restore artifact was not reclaimed: %s", entry.Name())
		}
	}
}

func prepareRecoveryFixture(t *testing.T) (dir, path, backup, candidate string, oldDigest, newDigest string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "ledger.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DB().Exec("INSERT INTO transactions (account,date,item,type,amount,balance) VALUES ('cash','2026-01-01','recovery','income',1,1)"); err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	backup = filepath.Join(dir, ".omni-money-restore-backup-recovery.db")
	if err := copyDatabaseFile(path, backup); err != nil {
		t.Fatal(err)
	}
	oldDigest, err = digestDatabaseFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	candidate = filepath.Join(dir, ".omni-money-restore-candidate-recovery.db")
	if err := copyDatabaseFile(path, candidate); err != nil {
		t.Fatal(err)
	}
	newDigest, err = digestDatabaseFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestStartupRecoveryRestoresSoleBackupBeforeFreshCreate(t *testing.T) {
	dir, path, backup, candidate, oldDigest, newDigest := prepareRecoveryFixture(t)
	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreManifest(path, restoreManifest{
		Version: restoreManifestVersion, Phase: "prepared", Current: filepath.Base(path),
		Backup: filepath.Base(backup), Candidate: filepath.Base(candidate),
		OldDigest: oldDigest, NewDigest: newDigest,
	}); err != nil {
		t.Fatal(err)
	}
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatalf("sole valid restore backup was not recovered: %v", err)
	}
	defer instance.Close()
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovered ledger count = %d, err=%v", count, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("recovery backup was not cleaned: %v", err)
	}
	if _, err := os.Stat(restoreManifestPath(path)); !os.IsNotExist(err) {
		t.Fatalf("restore manifest was not cleaned: %v", err)
	}
	_ = dir
}

func TestStartupRecoveryRejectsAmbiguousMissingLive(t *testing.T) {
	dir, path, backup, candidate, oldDigest, newDigest := prepareRecoveryFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreManifest(path, restoreManifest{
		Version: restoreManifestVersion, Phase: "prepared", Current: filepath.Base(path),
		Backup: filepath.Base(backup), Candidate: filepath.Base(candidate),
		OldDigest: oldDigest, NewDigest: newDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPlainInstance(path); err == nil {
		t.Fatal("startup accepted two valid restore images with no live database")
	}
	_ = dir
}

func TestStartupRecoveryRejectsUnjournaledThirdLiveImage(t *testing.T) {
	dir, path, backup, candidate, oldDigest, newDigest := prepareRecoveryFixture(t)
	thirdDir := filepath.Join(dir, "third-source")
	if err := os.Mkdir(thirdDir, 0700); err != nil {
		t.Fatal(err)
	}
	thirdPath := filepath.Join(thirdDir, "third.db")
	third, err := OpenPlainInstance(thirdPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.DB().Exec("INSERT INTO transactions (account,date,item,type,amount,balance) VALUES ('cash','2026-01-02','third','income',2,2)"); err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := copyDatabaseFile(thirdPath, path); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreManifest(path, restoreManifest{
		Version: restoreManifestVersion, Phase: "swapped", Current: filepath.Base(path),
		Backup: filepath.Base(backup), Candidate: filepath.Base(candidate),
		OldDigest: oldDigest, NewDigest: newDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPlainInstance(path); err == nil {
		t.Fatal("startup adopted a valid live image that was not named by the journal")
	}
}

func TestStartupRecoveryReplacesCorruptLiveWithValidBackup(t *testing.T) {
	dir, path, backup, candidate, oldDigest, newDigest := prepareRecoveryFixture(t)
	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt live"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreManifest(path, restoreManifest{
		Version: restoreManifestVersion, Phase: "swapped", Current: filepath.Base(path),
		Backup: filepath.Base(backup), Candidate: filepath.Base(candidate),
		OldDigest: oldDigest, NewDigest: newDigest,
	}); err != nil {
		t.Fatal(err)
	}
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatalf("valid backup did not replace corrupt live: %v", err)
	}
	defer instance.Close()
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovered ledger count = %d, err=%v", count, err)
	}
	_ = dir
}

func TestStartupRecoveryRejectsMissingLiveWithoutJournal(t *testing.T) {
	dir, path, backup, candidate, _, _ := prepareRecoveryFixture(t)
	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPlainInstance(path); err == nil {
		t.Fatal("startup created a fresh ledger over an unjournaled restore backup")
	}
	_ = dir
	_ = backup
}

func TestConcurrentSnapshotCreateListAndRestore(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	insertSnapshotTestTransaction(t, instance, "before")
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	insertSnapshotTestTransaction(t, instance, "after")

	start := make(chan struct{})
	errorsCh := make(chan error, 20)
	for range 8 {
		go func() {
			<-start
			for range 10 {
				if _, err := instance.ListSnapshots(snapshotDir); err != nil {
					errorsCh <- err
					return
				}
			}
			errorsCh <- nil
		}()
	}
	go func() {
		<-start
		_, createErr := instance.CreateSnapshot(snapshotDir)
		errorsCh <- createErr
	}()
	go func() {
		<-start
		errorsCh <- instance.RestoreSnapshot(snapshotDir, filepath.Base(snapshotPath))
	}()
	close(start)
	for range 10 {
		if err := <-errorsCh; err != nil {
			t.Fatalf("concurrent snapshot operation failed: %v", err)
		}
	}
}

func TestRollbackErrorsAreReturned(t *testing.T) {
	instance, dbPath, _ := newPlainSnapshotTestInstance(t)
	if err := instance.DB().Close(); err != nil {
		t.Fatal(err)
	}
	instance.mu.Lock()
	instance.db = nil
	instance.mu.Unlock()
	backupPath := filepath.Join(filepath.Dir(dbPath), "missing-backup.db")
	candidatePath := filepath.Join(filepath.Dir(dbPath), "candidate.db")
	if err := os.WriteFile(candidatePath, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rollbackRestoreFiles(dbPath, backupPath, candidatePath, instance)
	if err == nil {
		t.Fatal("rollback unexpectedly hid missing backup error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback error = %v, want missing-file cause", err)
	}
}
