//go:build windows

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"omni_money/backend/fileprivacy"
	"omni_money/backend/securedb"
)

func TestSyncOpenRestoreCandidateSupportsWindowsFlush(t *testing.T) {
	dir := t.TempDir()
	candidatePath, candidate, err := temporaryDatabaseFile(dir, ".omni-money-restore-candidate-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	defer os.Remove(candidatePath)
	if _, err := candidate.WriteString("restore candidate"); err != nil {
		t.Fatal(err)
	}
	if err := syncOpenFileAndDirectory(candidate, filepath.Dir(candidatePath)); err != nil {
		t.Fatalf("sync writable restore candidate descriptor: %v", err)
	}
}

func TestValidateSnapshotDACLAllowsDistinctOwnerPrincipalSets(t *testing.T) {
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		sddl   string
		owner  *windows.SID
		system *windows.SID
		aces   uint16
	}{
		{name: "LocalSystem owner uses one ACE", sddl: "D:P(A;;FA;;;SY)", owner: system, system: system, aces: 1},
		{name: "normal owner uses owner and system ACEs", sddl: "D:P(A;;FA;;;BA)(A;;FA;;;SY)", owner: administrators, system: system, aces: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if dacl.AceCount != test.aces {
				t.Fatalf("test DACL ACE count = %d, want %d", dacl.AceCount, test.aces)
			}
			if err := validateSnapshotDACL(dacl, test.owner, test.system); err != nil {
				t.Fatalf("private DACL rejected: %v", err)
			}
		})
	}
}

func TestListValidationAnchorBlocksWindowsCandidateReplacement(t *testing.T) {
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
	if err := fileprivacy.Harden(candidate); err != nil {
		t.Fatal(err)
	}
	if err := copyFileToOpenBounded(source, candidate, maxSnapshotValidationBytes); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(candidatePath)

	anchor, validationPath, err := openSnapshotValidationAnchor(lock, candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	if err := os.Rename(candidatePath, candidatePath+".detached"); err == nil {
		t.Fatal("Windows validation anchor allowed candidate pathname replacement")
	}
	validated, err := instance.opener.Open(t.Context(), validationPath, securedb.ImmutableReadOnly)
	if err != nil {
		t.Fatalf("anchored candidate did not open while replacement was denied: %v", err)
	}
	validationErr := instance.validateSnapshotDatabaseContext(t.Context(), validated, validationPath)
	closeErr := validated.Close()
	if validationErr != nil || closeErr != nil {
		t.Fatalf("anchored candidate validation failed: %v / %v", validationErr, closeErr)
	}
}

func TestSnapshotTransactionLockRejectsUnsafeWindowsRootAndPrivacyDrift(t *testing.T) {
	dir := t.TempDir()
	if err := fileprivacy.HardenDirectory(dir); err != nil {
		t.Fatal(err)
	}
	makeWindowsDirectoryUnsafe(t, dir)
	if lock, err := acquireSnapshotTransactionLock(context.Background(), dir); err == nil {
		lock.release()
		t.Fatal("unsafe Windows snapshot transaction root was accepted")
	}
	if err := fileprivacy.HardenDirectory(dir); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSnapshotTransactionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	makeWindowsDirectoryUnsafe(t, dir)
	if err := lock.verify(); err == nil {
		t.Fatal("Windows snapshot transaction root privacy drift was accepted")
	}
}

func makeWindowsDirectoryUnsafe(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}
