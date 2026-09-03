//go:build !windows

package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/securedb"
)

func TestListValidationDescriptorBindsSQLiteToCandidateAcrossReplacementAndABA(t *testing.T) {
	instance, _, snapshotDir := newPlainSnapshotTestInstance(t)
	snapshotPath, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSnapshotTransactionLock(context.Background(), snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()

	source, err := lock.openArtifact(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	candidate, candidatePath, err := lock.createTemporary(".omni-money-list-validation-test-", ".db")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFileToOpenBounded(source, candidate, maxSnapshotValidationBytes); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Sync(); err != nil {
		t.Fatal(err)
	}
	expectedInfo, err := candidate.Stat()
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest, err := digestOpenFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}

	anchor, validationPath, err := openSnapshotValidationAnchor(lock, candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	detached := candidatePath + ".detached"
	if err := os.Rename(candidatePath, detached); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(detached)
	if err := os.WriteFile(candidatePath, []byte("attacker replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(candidatePath)

	validated, err := instance.opener.Open(t.Context(), validationPath, securedb.ImmutableReadOnly)
	if err != nil {
		t.Fatalf("descriptor path did not open the copied candidate: %v", err)
	}
	validationErr := instance.validateSnapshotDatabaseContext(t.Context(), validated, validationPath)
	closeErr := validated.Close()
	if validationErr != nil || closeErr != nil {
		t.Fatalf("descriptor-bound SQLite validation failed: %v / %v", validationErr, closeErr)
	}
	if err := validateListSnapshotCandidateBoundary(lock, candidatePath, anchor, expectedInfo, expectedDigest); err == nil || !strings.Contains(err.Error(), "candidate") {
		t.Fatalf("replacement candidate boundary error=%v", err)
	}

	if err := os.Remove(candidatePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(detached, candidatePath); err != nil {
		t.Fatal(err)
	}
	if err := validateListSnapshotCandidateBoundary(lock, candidatePath, anchor, expectedInfo, expectedDigest); err != nil {
		t.Fatalf("restored D1 identity failed after D1-D2-D1 ABA: %v", err)
	}
}

func TestSnapshotTransactionLockRejectsUnsafeAcquiredRootAndPrivacyDrift(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshots")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireSnapshotTransactionLock(context.Background(), dir); err == nil {
		lock.release()
		t.Fatal("world-readable snapshot transaction root was accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := lock.verify(); err == nil {
		t.Fatal("snapshot transaction root privacy drift was accepted")
	}
}
