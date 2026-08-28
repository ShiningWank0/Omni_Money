package desktopaccount

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/securedb"
)

func TestLegacyMigrationCopiesEveryDatabaseAndRequiresRecoveryAcknowledgment(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	copyTestFile(t, filepath.Join(root, legacyDBFileName), filepath.Join(root, legacyDBFileName+".bak"))
	if err := os.Mkdir(filepath.Join(root, "snapshots"), 0700); err != nil {
		t.Fatal(err)
	}
	createLegacyTestDatabase(t, filepath.Join(root, "snapshots", "snapshot_20260828.db"))
	// Sidecars are members of the source tuple, not unknown artifacts. The
	// injected copier does not interpret them, but this proves the coordinator
	// archives and removes each known member without touching it before publish.
	if err := os.WriteFile(filepath.Join(root, legacyDBFileName+"-wal"), []byte("legacy-wal"), 0600); err != nil {
		t.Fatal(err)
	}

	c := newMigrationTestCoordinator(t, root)
	recovery, err := c.MigrateLegacy(testPassword)
	if err != nil {
		t.Fatalf("MigrateLegacy: %v", err)
	}
	defer clear(recovery)
	if len(recovery) != keyenvelope.RecoverySecretSize {
		t.Fatalf("recovery length = %d", len(recovery))
	}
	status := c.Status()
	if !status.Configured || status.Unlocked || !status.LegacyMigrationRequired || status.Role != RoleAdmin {
		t.Fatalf("pending-delivery status = %+v", status)
	}
	if _, err := c.Setup(testPassword); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending Setup error = %v", err)
	}
	if err := c.Unlock(testPassword); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending Unlock error = %v", err)
	}
	if _, err := c.Recover(recovery, testNewPassword); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending Recover error = %v", err)
	}
	if err := c.ChangePassword(testPassword, testNewPassword); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending ChangePassword error = %v", err)
	}
	if next, err := c.RotateRecovery(testPassword); !errors.Is(err, ErrMigrationPending) {
		clear(next)
		t.Fatalf("pending RotateRecovery error = %v", err)
	}
	if lease, err := c.Service(); !errors.Is(err, ErrMigrationPending) {
		if lease != nil {
			lease.Release()
		}
		t.Fatalf("pending Service error = %v", err)
	}
	if _, err := c.CreateSnapshot(); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending CreateSnapshot error = %v", err)
	}
	if _, err := c.ListSnapshots(); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending ListSnapshots error = %v", err)
	}
	if err := c.RestoreSnapshot("snapshot.db"); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending RestoreSnapshot error = %v", err)
	}
	if err := c.Lock(); !errors.Is(err, ErrMigrationPending) {
		t.Fatalf("pending Lock error = %v", err)
	}
	for _, source := range []string{
		legacyDBFileName,
		legacyDBFileName + "-wal",
		legacyDBFileName + ".bak",
		filepath.Join("snapshots", "snapshot_20260828.db"),
	} {
		if _, err := os.Lstat(filepath.Join(root, source)); !os.IsNotExist(err) {
			t.Fatalf("plaintext source %q remains: %v", source, err)
		}
	}
	journalContent, err := os.ReadFile(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journalContent, testPassword) ||
		bytes.Contains(journalContent, recovery) ||
		bytes.Contains(journalContent, []byte(base64.StdEncoding.EncodeToString(recovery))) ||
		bytes.Contains(journalContent, []byte(base64.RawURLEncoding.EncodeToString(recovery))) {
		t.Fatal("migration journal persisted a plaintext password or recovery secret")
	}
	journalInfo, err := os.Stat(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	if journalInfo.Mode().Perm() != 0600 {
		t.Fatalf("migration journal mode = %04o", journalInfo.Mode().Perm())
	}
	for _, destination := range []string{
		filepath.Join("vaults", c.manifest.VaultID, vaultDBFileName),
		filepath.Join("vaults", c.manifest.VaultID, "snapshots", "legacy-backup.db"),
		filepath.Join("vaults", c.manifest.VaultID, "snapshots", "snapshot_20260828.db"),
	} {
		info, err := os.Stat(filepath.Join(root, destination))
		if err != nil {
			t.Fatalf("destination %q: %v", destination, err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("destination %q mode = %04o", destination, info.Mode().Perm())
		}
	}
	if err := c.AcknowledgeRecovery(); err != nil {
		t.Fatalf("AcknowledgeRecovery: %v", err)
	}
	if !c.Status().Unlocked {
		t.Fatal("acknowledged migrated vault was not exposed as unlocked")
	}
	if c.Status().LegacyMigrationRequired {
		t.Fatal("legacy migration remained pending after acknowledgment")
	}
	if _, err := os.Lstat(filepath.Join(root, migrationFileName)); !os.IsNotExist(err) {
		t.Fatalf("migration journal remains after acknowledgment: %v", err)
	}
	if err := c.AcknowledgeRecovery(); err != nil {
		t.Fatalf("idempotent AcknowledgeRecovery: %v", err)
	}
}

func TestLegacyMigrationResumesCrashBoundariesWithSameRecoverySecret(t *testing.T) {
	for _, checkpoint := range []string{
		"journal-created",
		"member-archived",
		"artifact-copied",
		"manifest-published",
		"member-quarantined",
		"member-deleted",
		"archive-deleted",
		"delivery-published",
		"recovery-delivery-published",
	} {
		t.Run(checkpoint, func(t *testing.T) {
			root := t.TempDir()
			createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
			coordinator := newMigrationTestCoordinator(t, root)
			crash := errors.New("injected process crash")
			fired := false
			coordinator.migrationFailpoint = func(name string) error {
				if !fired && name == checkpoint {
					fired = true
					return crash
				}
				return nil
			}
			if recovery, err := coordinator.MigrateLegacy(testPassword); !errors.Is(err, crash) {
				clear(recovery)
				t.Fatalf("MigrateLegacy at %s error = %v", checkpoint, err)
			}
			if !fired {
				t.Fatalf("checkpoint %q was not reached", checkpoint)
			}

			journal, err := readMigrationJournal(filepath.Join(root, migrationFileName))
			if err != nil {
				t.Fatalf("read pending journal: %v", err)
			}
			wantRecovery, err := keyenvelope.UnwrapWithPassword(
				journal.RecoveryDeliveryEnvelope,
				testPassword,
				journal.Manifest.context(),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(wantRecovery)

			restarted := newMigrationTestCoordinator(t, root)
			status := restarted.Status()
			if !status.LegacyMigrationRequired || status.Unlocked {
				t.Fatalf("restart status = %+v", status)
			}
			if recovery, err := restarted.MigrateLegacy([]byte("definitely-wrong-password")); !errors.Is(err, keyenvelope.ErrAuthentication) {
				clear(recovery)
				t.Fatalf("wrong resume password error = %v", err)
			}
			gotRecovery, err := restarted.MigrateLegacy(testPassword)
			if err != nil {
				t.Fatalf("resume MigrateLegacy: %v", err)
			}
			defer clear(gotRecovery)
			if !bytes.Equal(gotRecovery, wantRecovery) {
				t.Fatal("resumed migration returned a different recovery secret")
			}
			if restarted.Status().Unlocked {
				t.Fatal("pending recovery acknowledgment was exposed as unlocked")
			}
			if err := restarted.AcknowledgeRecovery(); err != nil {
				t.Fatal(err)
			}
			if !restarted.Status().Unlocked {
				t.Fatal("acknowledged resumed migration did not unlock the vault")
			}
		})
	}
}

func TestLegacyMigrationFailsClosedForChangedPendingSourceAndStrictJournal(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	c := newMigrationTestCoordinator(t, root)
	c.migrationFailpoint = func(name string) error {
		if name == "journal-created" {
			return errors.New("stop")
		}
		return nil
	}
	if _, err := c.MigrateLegacy(testPassword); err == nil {
		t.Fatal("MigrateLegacy unexpectedly completed")
	}
	if err := os.WriteFile(filepath.Join(root, legacyDBFileName), []byte(plaintextSQLiteHeader+"changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newMigrationTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("New with changed pending source error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte("\"version\": 1"), []byte("\"version\": 1, \"version\": 1"), 1)
	if err := os.WriteFile(filepath.Join(root, migrationFileName), content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newMigrationTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
		t.Fatalf("duplicate journal field error = %v", err)
	}
}

func TestLegacyMigrationDoesNotAdoptMismatchedPublishedDestination(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	coordinator := newMigrationTestCoordinator(t, root)
	crash := errors.New("stop after destination publication")
	coordinator.migrationFailpoint = func(name string) error {
		if name == "artifact-copied" {
			return crash
		}
		return nil
	}
	if _, err := coordinator.MigrateLegacy(testPassword); !errors.Is(err, crash) {
		t.Fatalf("MigrateLegacy error = %v", err)
	}
	journal, err := readMigrationJournal(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, filepath.FromSlash(journal.Artifacts[0].Destination))
	if err := os.WriteFile(destination, []byte(plaintextSQLiteHeader+"not-the-source"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := newMigrationTestCoordinator(t, root)
	if recovery, err := restarted.MigrateLegacy(testPassword); err == nil || !strings.Contains(err.Error(), "differ") {
		clear(recovery)
		t.Fatalf("mismatched destination resume error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyDBFileName)); err != nil {
		t.Fatalf("plaintext source was removed after mismatched adoption: %v", err)
	}
}

func TestLegacyMigrationFailsClosedIfQuarantinedSourceReappears(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	coordinator := newMigrationTestCoordinator(t, root)
	crash := errors.New("stop after quarantine journal")
	coordinator.migrationFailpoint = func(name string) error {
		if name == "quarantine-journaled" {
			return crash
		}
		return nil
	}
	if _, err := coordinator.MigrateLegacy(testPassword); !errors.Is(err, crash) {
		t.Fatalf("MigrateLegacy error = %v", err)
	}
	journal, err := readMigrationJournal(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	member := journal.Artifacts[0].Members[0]
	copyTestFile(
		t,
		filepath.Join(root, filepath.FromSlash(member.Quarantine)),
		filepath.Join(root, filepath.FromSlash(member.Source)),
	)
	if _, err := newMigrationTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "reappeared") {
		t.Fatalf("New with reappeared quarantined source error = %v", err)
	}
}

func TestMigrationJournalRejectsUnknownMissingAndTrailingJSON(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	coordinator := newMigrationTestCoordinator(t, root)
	coordinator.migrationFailpoint = func(name string) error {
		if name == "journal-created" {
			return errors.New("stop")
		}
		return nil
	}
	if _, err := coordinator.MigrateLegacy(testPassword); err == nil {
		t.Fatal("MigrateLegacy unexpectedly completed")
	}
	original, err := os.ReadFile(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"unknown": func(content []byte) []byte {
			return bytes.Replace(content, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1)
		},
		"missing": func(content []byte) []byte {
			return bytes.Replace(content, []byte("  \"phase\": \"copying\",\n"), nil, 1)
		},
		"trailing": func(content []byte) []byte {
			return append(append([]byte(nil), content...), []byte("{}\n")...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			target := t.TempDir()
			copyTestFile(t, filepath.Join(root, legacyDBFileName), filepath.Join(target, legacyDBFileName))
			if err := os.WriteFile(filepath.Join(target, migrationFileName), mutate(original), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := newMigrationTestCoordinatorResult(target); err == nil {
				t.Fatal("strict migration journal decoder accepted malformed JSON")
			}
		})
	}
}

func TestConfiguredManifestRejectsRemainingLegacyPlaintext(t *testing.T) {
	root := t.TempDir()
	coordinator := newMigrationTestCoordinator(t, root)
	recovery, err := coordinator.Setup(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	clear(recovery)
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	if _, err := newMigrationTestCoordinatorResult(root); err == nil || !strings.Contains(err.Error(), "beside a configured encrypted account") {
		t.Fatalf("New with manifest and legacy plaintext error = %v", err)
	}
}

func TestMigrationProcessLockAndStaleCoordinatorRefresh(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	first := newMigrationTestCoordinator(t, root)
	stale := newMigrationTestCoordinator(t, root)

	release, err := acquireMigrationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if recovery, err := first.MigrateLegacy(testPassword); !errors.Is(err, ErrBusy) {
		clear(recovery)
		t.Fatalf("concurrent MigrateLegacy error = %v", err)
	}
	release()

	stop := errors.New("stop after initial journal")
	first.migrationFailpoint = func(name string) error {
		if name == "journal-created" {
			return stop
		}
		return nil
	}
	if _, err := first.MigrateLegacy(testPassword); !errors.Is(err, stop) {
		t.Fatalf("first MigrateLegacy error = %v", err)
	}
	recovery, err := stale.MigrateLegacy(testPassword)
	if err != nil {
		t.Fatalf("stale coordinator did not reload pending journal: %v", err)
	}
	defer clear(recovery)
	if err := stale.AcknowledgeRecovery(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFailsClosedWhenDurableJournalDisappears(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	coordinator := newMigrationTestCoordinator(t, root)
	stop := errors.New("stop after journal")
	coordinator.migrationFailpoint = func(name string) error {
		if name == "journal-created" {
			return stop
		}
		return nil
	}
	if _, err := coordinator.MigrateLegacy(testPassword); !errors.Is(err, stop) {
		t.Fatalf("MigrateLegacy error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, migrationFileName)); err != nil {
		t.Fatal(err)
	}
	if recovery, err := coordinator.MigrateLegacy(testPassword); err == nil || !errors.Is(err, ErrMigrationState) {
		clear(recovery)
		t.Fatalf("MigrateLegacy after journal disappearance error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyDBFileName)); err != nil {
		t.Fatalf("legacy source changed after journal disappearance: %v", err)
	}
}

func TestMigrationResumesKnownPrivateTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	coordinator := newMigrationTestCoordinator(t, root)
	stop := errors.New("stop after journal")
	coordinator.migrationFailpoint = func(name string) error {
		if name == "journal-created" {
			return stop
		}
		return nil
	}
	if _, err := coordinator.MigrateLegacy(testPassword); !errors.Is(err, stop) {
		t.Fatalf("MigrateLegacy error = %v", err)
	}
	journal, err := readMigrationJournal(filepath.Join(root, migrationFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".desktop-migration.tmp-crash", ".desktop-account.tmp-crash"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	archiveDirectory := filepath.Join(root, filepath.Dir(filepath.FromSlash(journal.Artifacts[0].Members[0].Archive)))
	if err := ensurePrivateDirectory(archiveDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveDirectory, ".legacy-archive.tmp-crash"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := newMigrationTestCoordinator(t, root)
	recovery, err := restarted.MigrateLegacy(testPassword)
	if err != nil {
		t.Fatalf("resume with known private temporary files: %v", err)
	}
	clear(recovery)
	for _, path := range []string{
		filepath.Join(root, ".desktop-migration.tmp-crash"),
		filepath.Join(root, ".desktop-account.tmp-crash"),
		filepath.Join(archiveDirectory, ".legacy-archive.tmp-crash"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary file remains %q: %v", path, err)
		}
	}
}

func TestLegacyMigrationRejectsInitialOrphanAndUnknownArtifacts(t *testing.T) {
	for _, artifact := range []string{"vaults", legacyQuarantineDir, legacyWorkDir, manifestFileName, "unknown.txt"} {
		t.Run(artifact, func(t *testing.T) {
			root := t.TempDir()
			createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
			path := filepath.Join(root, artifact)
			if strings.Contains(filepath.Base(artifact), ".") {
				if err := os.WriteFile(path, []byte("orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			coordinator, newErr := newMigrationTestCoordinatorResult(root)
			if newErr != nil {
				if artifact == manifestFileName {
					return
				}
				t.Fatalf("New with orphan %q: %v", artifact, newErr)
			}
			t.Cleanup(func() { _ = coordinator.Close() })
			if recovery, err := coordinator.MigrateLegacy(testPassword); err == nil || !strings.Contains(err.Error(), "unrecognized") {
				clear(recovery)
				t.Fatalf("MigrateLegacy with orphan %q error = %v", artifact, err)
			}
			if _, err := os.Stat(filepath.Join(root, legacyDBFileName)); err != nil {
				t.Fatalf("legacy source was changed: %v", err)
			}
		})
	}
}

func newMigrationTestCoordinator(t *testing.T, root string) *Coordinator {
	t.Helper()
	c, err := newMigrationTestCoordinatorResult(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newMigrationTestCoordinatorResult(root string) (*Coordinator, error) {
	opener := func(path string, key securedb.RawKey) (*database.Instance, error) {
		defer key.Destroy()
		return database.OpenPlainInstance(path)
	}
	c, err := newCoordinator(root, opener, opener, func(string) error { return nil })
	if err != nil {
		return nil, err
	}
	c.copyPlaintext = func(sourcePath, destinationPath string, _ *securedb.Opener) error {
		return copyMigrationTestFile(sourcePath, destinationPath)
	}
	c.verifyPlaintext = func(sourcePath, destinationPath string, _ *securedb.Opener) error {
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		destination, err := os.ReadFile(destinationPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(source, destination) {
			return errors.New("test migration copies differ")
		}
		return nil
	}
	c.verifyEncrypted = func(string, *securedb.Opener) error { return nil }
	return c, nil
}

func createLegacyTestDatabase(t *testing.T, path string) {
	t.Helper()
	instance, err := database.OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func copyTestFile(t *testing.T, source, destination string) {
	t.Helper()
	if err := copyMigrationTestFile(source, destination); err != nil {
		t.Fatal(err)
	}
}

func copyMigrationTestFile(source, destination string) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	defer clear(content)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
