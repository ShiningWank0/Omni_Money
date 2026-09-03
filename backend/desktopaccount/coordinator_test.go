package desktopaccount

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/securedb"
)

var (
	testPassword    = []byte("desktop-test-password-one")
	testNewPassword = []byte("desktop-test-password-two")
)

func TestCoordinatorLifecycleAndCredentialRotation(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir())
	status := c.Status()
	if status.Configured || status.Unlocked || status.LegacyMigrationRequired || status.Role != "" {
		t.Fatalf("unexpected initial status: %+v", status)
	}
	if _, err := c.Service(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("pre-setup Service error = %v", err)
	}

	recovery, err := c.Setup(testPassword)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer clear(recovery)
	if len(recovery) != keyenvelope.RecoverySecretSize {
		t.Fatalf("recovery secret length = %d", len(recovery))
	}
	status = c.Status()
	if !status.Configured || !status.Unlocked || status.LegacyMigrationRequired || status.Role != RoleAdmin {
		t.Fatalf("unexpected setup status: %+v", status)
	}
	if _, err := c.Setup(testPassword); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second Setup error = %v", err)
	}

	lease, err := c.Service()
	if err != nil {
		t.Fatalf("Service: %v", err)
	}
	service, err := lease.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if _, err := service.GetAccounts(); err != nil {
		t.Fatalf("guarded service GetAccounts: %v", err)
	}
	lockDone := make(chan error, 1)
	go func() { lockDone <- c.Lock() }()
	waitForDraining(t, c)
	select {
	case err := <-lockDone:
		t.Fatalf("Lock returned before in-flight lease release: %v", err)
	default:
	}
	lease.Release()
	if err := <-lockDone; err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if _, err := lease.Core(); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("released Core error = %v", err)
	}
	if _, err := service.GetAccounts(); !errors.Is(err, core.ErrServiceUnavailable) {
		t.Fatalf("released guarded service error = %v", err)
	}
	if c.Status().Unlocked {
		t.Fatal("status remained unlocked after Lock")
	}

	if err := c.Unlock([]byte("wrong-desktop-password")); !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("wrong-password Unlock error = %v", err)
	}
	if err := c.Unlock(testPassword); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := c.ChangePassword(testPassword, testNewPassword); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	rotated, err := c.RotateRecovery(testNewPassword)
	if err != nil {
		t.Fatalf("RotateRecovery: %v", err)
	}
	defer clear(rotated)
	if bytes.Equal(rotated, recovery) {
		t.Fatal("recovery rotation returned the previous secret")
	}
	if err := c.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := c.Unlock(testPassword); !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("old password remained active: %v", err)
	}
	if _, err := c.Recover(recovery, testPassword); !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("old recovery secret remained active: %v", err)
	}
	nextRecovery, err := c.Recover(rotated, testPassword)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	defer clear(nextRecovery)
	if !c.Status().Unlocked {
		t.Fatal("Recover did not unlock the account")
	}
	if err := c.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := c.Unlock(testPassword); err != nil {
		t.Fatalf("recovery replacement password did not unlock: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if err := c.Unlock(testPassword); !errors.Is(err, ErrClosed) {
		t.Fatalf("Unlock after Close error = %v", err)
	}
}

func TestCoordinatorSnapshotLifecycle(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir())
	recovery, err := c.Setup(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	clear(recovery)
	t.Cleanup(func() { _ = c.Close() })

	path, err := c.CreateSnapshot()
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if filepath.Ext(path) != ".db" {
		t.Fatalf("snapshot path = %q", path)
	}
	snapshots, err := c.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0] != filepath.Base(path) {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if err := c.RestoreSnapshot(filepath.Base(path)); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	lease, err := c.Service()
	if err != nil {
		t.Fatalf("Service after restore: %v", err)
	}
	lease.Release()
}

func TestCoordinatorSnapshotFailureForcesLockAndReauthentication(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir())
	recovery, err := c.Setup(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	clear(recovery)
	t.Cleanup(func() { _ = c.Close() })
	path, err := c.CreateSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreSnapshot(filepath.Base(path)); err == nil {
		t.Fatal("corrupt snapshot restore unexpectedly succeeded")
	}
	if c.Status().Unlocked {
		t.Fatal("failed restore left the Desktop coordinator unlocked")
	}
	if _, err := c.Service(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Service after failed restore = %v, want ErrLocked", err)
	}
}

func TestCoordinatorRestartBeginsLockedAndReopensPersistedAccount(t *testing.T) {
	root := t.TempDir()
	first := newTestCoordinator(t, root)
	recovery, err := first.Setup(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	clear(recovery)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newTestCoordinator(t, root)
	t.Cleanup(func() { _ = restarted.Close() })
	status := restarted.Status()
	if !status.Configured || status.Unlocked || status.Role != RoleAdmin || status.LegacyMigrationRequired {
		t.Fatalf("restart status = %+v", status)
	}
	if _, err := restarted.Service(); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked restart Service error = %v", err)
	}
	if err := restarted.Unlock(testPassword); err != nil {
		t.Fatalf("Unlock after restart: %v", err)
	}
}

func TestCoordinatorPasswordPolicyIsEnforcedByPackage(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir())
	for _, password := range [][]byte{
		bytes.Repeat([]byte{'x'}, minPasswordBytes-1),
		bytes.Repeat([]byte{'x'}, maxPasswordBytes+1),
	} {
		if _, err := c.Setup(password); !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("Setup password length %d error = %v", len(password), err)
		}
	}
	recovery, err := c.Setup(bytes.Repeat([]byte{'x'}, minPasswordBytes))
	if err != nil {
		t.Fatal(err)
	}
	clear(recovery)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.ChangePassword(bytes.Repeat([]byte{'x'}, minPasswordBytes), []byte("short")); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("ChangePassword policy error = %v", err)
	}
}

func TestLegacyDatabaseRequiresExplicitMigration(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, legacyDBFileName)
	// Older releases relied on process defaults, so an insecure legacy file
	// can be 0644. New() first secures the containing root to 0700 and must
	// still recognize the file for the explicit migration flow.
	if err := os.WriteFile(legacyPath, []byte("SQLite format 3\x00legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	c := newTestCoordinator(t, root)
	status := c.Status()
	if status.Configured || status.Unlocked || !status.LegacyMigrationRequired {
		t.Fatalf("legacy status = %+v", status)
	}
	if _, err := c.Setup(testPassword); !errors.Is(err, ErrLegacyMigrationRequired) {
		t.Fatalf("Setup with legacy DB error = %v", err)
	}
	content, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "SQLite format 3\x00legacy" {
		t.Fatalf("legacy database was modified: %q", content)
	}
}

func TestUnrecognizedAndOrphanLegacyArtifactsFailClosed(t *testing.T) {
	for name, content := range map[string][]byte{
		"truncated":    []byte("SQLite"),
		"unrecognized": bytes.Repeat([]byte{0xa5}, 64),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, legacyDBFileName), content, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := newTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "recognized") {
				t.Fatalf("New error = %v", err)
			}
			persisted, err := os.ReadFile(filepath.Join(root, legacyDBFileName))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(persisted, content) {
				t.Fatal("unrecognized database was modified")
			}
		})
	}

	for _, artifact := range []string{legacyDBFileName + ".bak", "snapshots", "vaults"} {
		t.Run("orphan "+artifact, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, artifact)
			if artifact == "snapshots" || artifact == "vaults" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("legacy"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := newTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "refusing automatic setup") {
				t.Fatalf("New error = %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("orphan artifact was changed: %v", err)
			}
		})
	}
}

func TestManifestIsPrivateStrictAndContainsNoPlaintextSecrets(t *testing.T) {
	root := t.TempDir()
	c := newTestCoordinator(t, root)
	recovery, err := c.Setup(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(recovery)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, manifestFileName)
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("manifest mode = %04o", info.Mode().Perm())
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, testPassword) || bytes.Contains(content, recovery) || bytes.Contains(content, []byte(base64.StdEncoding.EncodeToString(recovery))) || bytes.Contains(content, []byte(base64.RawURLEncoding.EncodeToString(recovery))) {
		t.Fatal("manifest contains a plaintext password or recovery secret")
	}
	if strings.Contains(string(content), "omni_money.db") {
		t.Fatal("manifest persisted a filesystem path")
	}

	t.Run("unknown field", func(t *testing.T) {
		targetRoot := copyManifestRoot(t, root, content)
		path := filepath.Join(targetRoot, manifestFileName)
		modified := bytes.Replace(content, []byte("{\n"), []byte("{\n  \"unexpected\": true,\n"), 1)
		if err := os.WriteFile(path, modified, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newTestCoordinatorResult(targetRoot); err == nil || !strings.Contains(err.Error(), "is not allowed") {
			t.Fatalf("unknown field error = %v", err)
		}
	})

	t.Run("field name casing", func(t *testing.T) {
		targetRoot := copyManifestRoot(t, root, content)
		path := filepath.Join(targetRoot, manifestFileName)
		modified := bytes.Replace(content, []byte("\"version\": 1"), []byte("\"Version\": 1"), 1)
		if err := os.WriteFile(path, modified, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newTestCoordinatorResult(targetRoot); err == nil || !strings.Contains(err.Error(), "required field") {
			t.Fatalf("case-folded field error = %v", err)
		}
	})

	t.Run("duplicate field", func(t *testing.T) {
		targetRoot := copyManifestRoot(t, root, content)
		path := filepath.Join(targetRoot, manifestFileName)
		modified := bytes.Replace(content, []byte("\"version\": 1"), []byte("\"version\": 1, \"version\": 1"), 1)
		if err := os.WriteFile(path, modified, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newTestCoordinatorResult(targetRoot); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
			t.Fatalf("duplicate field error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privileges vary on Windows")
		}
		targetRoot := t.TempDir()
		outside := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(outside, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(targetRoot, manifestFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := newTestCoordinatorResult(targetRoot); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink manifest error = %v", err)
		}
	})
}

func newTestCoordinator(t *testing.T, root string) *Coordinator {
	t.Helper()
	c, err := newTestCoordinatorResult(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestCoordinatorResult(root string) (*Coordinator, error) {
	opener := func(path string, key securedb.RawKey) (*database.Instance, error) {
		defer key.Destroy()
		return database.OpenPlainInstance(path)
	}
	return newCoordinator(root, opener, opener, func(string) error { return nil })
}

func copyManifestRoot(t *testing.T, _ string, content []byte) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, manifestFileName), content, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func waitForDraining(t *testing.T, c *Coordinator) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		draining := c.draining
		c.mu.Unlock()
		if draining {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for coordinator to drain")
		}
		time.Sleep(time.Millisecond)
	}
}
