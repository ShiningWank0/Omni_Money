package desktopaccount

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"omni_money/backend/database"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/securedb"
)

const (
	migrationVersion        = 1
	maxMigrationBytes       = 1024 * 1024
	migrationPhaseCopying   = "copying"
	migrationPhasePublished = "published"
	migrationPhaseCleanup   = "cleanup"
	migrationPhaseDelivery  = "delivery"
	legacyQuarantineDir     = "legacy-quarantine"
	legacyWorkDir           = "migration-work"
)

var ErrMigrationState = errors.New("legacy Desktop migration state is invalid")

type migrationArtifact struct {
	Destination string            `json:"destination"`
	Copied      bool              `json:"copied"`
	Members     []migrationMember `json:"members"`
}

type migrationMember struct {
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	Archive        string `json:"archive"`
	Quarantine     string `json:"quarantine"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Archived       bool   `json:"archived"`
	Quarantined    bool   `json:"quarantined"`
	Deleted        bool   `json:"deleted"`
	ArchiveDeleted bool   `json:"archive_deleted"`
}

type migrationJournal struct {
	Version                  int                   `json:"version"`
	MigrationID              string                `json:"migration_id"`
	Phase                    string                `json:"phase"`
	Manifest                 *manifest             `json:"manifest"`
	RecoveryDeliveryEnvelope *keyenvelope.Envelope `json:"recovery_delivery_envelope"`
	Artifacts                []migrationArtifact   `json:"artifacts"`
}

func defaultPlaintextCopier(sourcePath, destinationPath string, opener *securedb.Opener) error {
	return securedb.CopyPlaintextToEncrypted(context.Background(), sourcePath, destinationPath, opener)
}

func defaultPlaintextVerifier(sourcePath, destinationPath string, opener *securedb.Opener) error {
	return securedb.VerifyPlaintextMatchesEncrypted(context.Background(), sourcePath, destinationPath, opener)
}

func defaultEncryptedVerifier(path string, opener *securedb.Opener) error {
	if err := securedb.RequireEncryptedHeader(path); err != nil {
		return err
	}
	db, err := opener.Open(context.Background(), path, securedb.ReadOnly)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := opener.CheckIntegrity(context.Background(), db); err != nil {
		return err
	}
	rows, err := db.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("encrypted migration destination has a foreign key violation")
	}
	return rows.Err()
}

// MigrateLegacy converts every recognized legacy plaintext database into the
// new SQLCipher vault. Every durable boundary is journaled, so the same
// password can resume after a crash and returns the same recovery secret.
// The account is left unlocked on success.
func (c *Coordinator) MigrateLegacy(password []byte) (recoverySecret []byte, err error) {
	passwordCopy, err := copyPassword(password)
	if err != nil {
		return nil, err
	}
	defer clear(passwordCopy)
	if c == nil {
		return nil, ErrClosed
	}
	releaseProcessLock, err := acquireMigrationLock(c.root)
	if err != nil {
		return nil, err
	}
	defer releaseProcessLock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := cleanupRootMigrationTemps(c.root); err != nil {
		return nil, err
	}
	if err := c.refreshMigrationStateLocked(); err != nil {
		return nil, err
	}
	if c.migration != nil {
		if err := cleanupJournalMigrationTemps(c.root, c.migration); err != nil {
			return nil, err
		}
	}
	if err := c.requireMutableLocked(); err != nil {
		return nil, err
	}
	if c.migration == nil {
		if c.manifest != nil {
			return nil, ErrAlreadyConfigured
		}
		if !c.legacyMigration {
			return nil, ErrNotConfigured
		}
		journal, secret, createErr := c.createMigrationLocked(passwordCopy)
		if createErr != nil {
			return nil, createErr
		}
		clear(secret)
		c.migration = journal
		if err := c.migrationCheckpointLocked("journal-created"); err != nil {
			return nil, err
		}
	}
	journal := c.migration
	context := journal.Manifest.context()
	dek, err := keyenvelope.UnwrapWithPassword(journal.Manifest.PasswordEnvelope, passwordCopy, context)
	if err != nil {
		clear(dek)
		return nil, err
	}
	defer clear(dek)
	recoverySecret, err = keyenvelope.UnwrapWithPassword(journal.RecoveryDeliveryEnvelope, passwordCopy, context)
	if err != nil {
		clear(recoverySecret)
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			clear(recoverySecret)
			recoverySecret = nil
		}
	}()
	if len(recoverySecret) != keyenvelope.RecoverySecretSize {
		return nil, fmt.Errorf("%w: recovery delivery has the wrong size", ErrMigrationState)
	}
	recoveryDEK, err := keyenvelope.UnwrapWithRecovery(journal.Manifest.RecoveryEnvelope, recoverySecret, context)
	if err != nil {
		clear(recoveryDEK)
		return nil, fmt.Errorf("%w: authenticate recovery envelope: %v", ErrMigrationState, err)
	}
	if !bytes.Equal(dek, recoveryDEK) {
		clear(recoveryDEK)
		return nil, fmt.Errorf("%w: migration envelopes do not contain the same vault key", ErrMigrationState)
	}
	clear(recoveryDEK)

	if err := journal.validateFilesystem(c.root, c.manifest != nil); err != nil {
		return nil, err
	}
	key, err := securedb.NewRawKey(dek)
	if err != nil {
		return nil, err
	}
	opener := securedb.NewEncryptedOpener(key)
	key.Destroy()
	defer opener.Destroy()
	if journal.Phase == migrationPhaseCopying {
		if err := c.resumeCopiesLocked(journal, opener); err != nil {
			return nil, err
		}
	}

	if journal.Phase == migrationPhaseCopying {
		if err := c.verifyMigrationDestinationsLocked(journal, opener, true); err != nil {
			return nil, err
		}
		if c.manifest == nil {
			if err := writeManifestAtomic(c.manifestPath, journal.Manifest); err != nil {
				return nil, err
			}
			c.manifest = journal.Manifest
			if err := c.migrationCheckpointLocked("manifest-published"); err != nil {
				return nil, err
			}
		}
		journal.Phase = migrationPhasePublished
		if err := c.writeMigrationLocked(); err != nil {
			return nil, err
		}
		if err := c.migrationCheckpointLocked("delivery-published"); err != nil {
			return nil, err
		}
	}
	if journal.Phase == migrationPhasePublished {
		if err := c.verifyMigrationDestinationsLocked(journal, opener, true); err != nil {
			return nil, err
		}
		journal.Phase = migrationPhaseCleanup
		if err := c.writeMigrationLocked(); err != nil {
			return nil, err
		}
	}
	if journal.Phase == migrationPhaseCleanup {
		if err := c.verifyMigrationDestinationsLocked(journal, opener, false); err != nil {
			return nil, err
		}
		if err := c.resumeCleanupLocked(journal); err != nil {
			return nil, err
		}
		if err := c.validateLegacyCleanupLocked(journal); err != nil {
			return nil, err
		}
		if err := c.verifyMigrationDestinationsLocked(journal, opener, false); err != nil {
			return nil, err
		}
		journal.Phase = migrationPhaseDelivery
		if err := c.writeMigrationLocked(); err != nil {
			return nil, err
		}
		if err := c.migrationCheckpointLocked("recovery-delivery-published"); err != nil {
			return nil, err
		}
	}
	if journal.Phase != migrationPhaseDelivery {
		return nil, fmt.Errorf("%w: unsupported migration phase %q", ErrMigrationState, journal.Phase)
	}
	if err := c.verifyMigrationDestinationsLocked(journal, opener, false); err != nil {
		return nil, err
	}
	if c.instance == nil {
		activePath := filepath.Join(c.root, journal.Artifacts[0].Destination)
		openKey, keyErr := securedb.NewRawKey(dek)
		if keyErr != nil {
			return nil, keyErr
		}
		instance, openErr := c.openExisting(activePath, openKey)
		openKey.Destroy()
		if openErr != nil {
			return nil, fmt.Errorf("open migrated Desktop vault: %w", openErr)
		}
		c.instance = instance
		c.generation++
	}
	c.legacyMigration = true
	succeeded = true
	return recoverySecret, nil
}

// AcknowledgeRecovery durably records that the one-time recovery secret was
// saved. It is idempotent when no migration delivery remains pending.
func (c *Coordinator) AcknowledgeRecovery() error {
	if c == nil {
		return nil
	}
	releaseProcessLock, err := acquireMigrationLock(c.root)
	if err != nil {
		return err
	}
	defer releaseProcessLock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := cleanupRootMigrationTemps(c.root); err != nil {
		return err
	}
	if err := c.refreshMigrationStateLocked(); err != nil {
		return err
	}
	if c.migration != nil {
		if err := cleanupJournalMigrationTemps(c.root, c.migration); err != nil {
			return err
		}
	}
	if err := c.requireMutableLocked(); err != nil {
		return err
	}
	if c.migration == nil {
		return nil
	}
	if c.migration.Phase != migrationPhaseDelivery || c.manifest == nil {
		return fmt.Errorf("%w: recovery delivery is not ready", ErrMigrationState)
	}
	quarantine := filepath.Join(c.root, legacyQuarantineDir, c.migration.MigrationID)
	if err := removeEmptyDirectoryTree(quarantine, filepath.Join(c.root, legacyQuarantineDir)); err != nil {
		return err
	}
	journalPath := filepath.Join(c.root, migrationFileName)
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove Desktop migration journal: %w", err)
	}
	if err := syncDirectory(c.root); err != nil {
		return fmt.Errorf("sync Desktop migration acknowledgment: %w", err)
	}
	c.migration = nil
	c.legacyMigration = false
	return nil
}

func (c *Coordinator) createMigrationLocked(password []byte) (*migrationJournal, []byte, error) {
	installationID, err := generateID()
	if err != nil {
		return nil, nil, err
	}
	userID, err := generateID()
	if err != nil {
		return nil, nil, err
	}
	vaultID, err := generateID()
	if err != nil {
		return nil, nil, err
	}
	migrationID, err := generateID()
	if err != nil {
		return nil, nil, err
	}
	ctx := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	dek, err := keyenvelope.GenerateDEK()
	if err != nil {
		return nil, nil, err
	}
	defer clear(dek)
	recovery, err := keyenvelope.GenerateRecoverySecret()
	if err != nil {
		return nil, nil, err
	}
	keepRecovery := false
	defer func() {
		if !keepRecovery {
			clear(recovery)
		}
	}()
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, password, ctx)
	if err != nil {
		return nil, nil, err
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, recovery, ctx)
	if err != nil {
		return nil, nil, err
	}
	deliveryEnvelope, err := keyenvelope.WrapWithPassword(recovery, password, ctx)
	if err != nil {
		return nil, nil, err
	}
	document := &manifest{
		Version: manifestVersion, InstallationID: installationID, UserID: userID,
		VaultID: vaultID, Role: RoleAdmin, Generation: 1,
		PasswordEnvelope: passwordEnvelope, RecoveryEnvelope: recoveryEnvelope,
	}
	artifacts, err := discoverMigrationArtifacts(c.root, vaultID, migrationID)
	if err != nil {
		return nil, nil, err
	}
	journal := &migrationJournal{
		Version: migrationVersion, MigrationID: migrationID, Phase: migrationPhaseCopying,
		Manifest: document, RecoveryDeliveryEnvelope: deliveryEnvelope, Artifacts: artifacts,
	}
	if err := journal.validate(); err != nil {
		return nil, nil, err
	}
	if err := writeMigrationJournalAtomic(filepath.Join(c.root, migrationFileName), journal); err != nil {
		return nil, nil, err
	}
	keepRecovery = true
	return journal, recovery, nil
}

func (c *Coordinator) resumeCopiesLocked(journal *migrationJournal, opener *securedb.Opener) error {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		if len(artifact.Members) == 0 {
			return fmt.Errorf("%w: migration artifact has no source tuple", ErrMigrationState)
		}
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			source := filepath.Join(c.root, filepath.FromSlash(member.Source))
			archive := filepath.Join(c.root, filepath.FromSlash(member.Archive))
			if member.Archived {
				if member.ArchiveDeleted {
					return fmt.Errorf("%w: work archive was deleted before publication", ErrMigrationState)
				}
				if err := verifyMemberFingerprint(archive, member, false); err != nil {
					return err
				}
				continue
			}
			if err := verifyMemberFingerprint(source, member, member.Kind == "main"); err != nil {
				return err
			}
			if _, err := os.Lstat(archive); err == nil {
				if err := verifyMemberFingerprint(archive, member, false); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			} else {
				if err := ensurePrivatePathDirectories(c.root, archive); err != nil {
					return err
				}
				if err := copyRegularFileDurable(source, archive); err != nil {
					return fmt.Errorf("archive legacy SQLite %s %q: %w", member.Kind, member.Source, err)
				}
				if err := c.migrationCheckpointLocked("member-archived"); err != nil {
					return err
				}
				if err := verifyMemberFingerprint(archive, member, false); err != nil {
					return err
				}
				// Prove the complete source tuple remained stable while it was
				// physically archived; otherwise never combine torn generations.
				for checkIndex := 0; checkIndex <= memberIndex; checkIndex++ {
					check := &artifact.Members[checkIndex]
					if err := verifyMemberFingerprint(filepath.Join(c.root, filepath.FromSlash(check.Source)), check, check.Kind == "main"); err != nil {
						return err
					}
				}
			}
			member.Archived = true
			if err := c.writeMigrationLocked(); err != nil {
				return err
			}
		}
		// Recheck every original after the whole tuple is durable. This makes a
		// concurrent old process a fail-closed condition instead of producing a
		// mixed main/WAL generation.
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			if err := verifyMemberFingerprint(filepath.Join(c.root, filepath.FromSlash(member.Source)), member, member.Kind == "main"); err != nil {
				return err
			}
		}
		archiveSource := filepath.Join(c.root, filepath.FromSlash(artifact.Members[0].Archive))
		destination := filepath.Join(c.root, filepath.FromSlash(artifact.Destination))
		if artifact.Copied {
			if err := validatePrivateVaultFile(destination); err != nil {
				return fmt.Errorf("validate migrated copy %q: %w", artifact.Destination, err)
			}
			if err := c.verifyVault(destination); err != nil {
				return fmt.Errorf("verify migrated copy %q: %w", artifact.Destination, err)
			}
			if err := c.verifyPlaintext(archiveSource, destination, opener); err != nil {
				return fmt.Errorf("compare resumed migrated copy %q: %w", artifact.Destination, err)
			}
			continue
		}
		if _, err := os.Lstat(destination); err == nil {
			// Copy publishes only after full source/target logical verification.
			// An encrypted destination here is the crash boundary after publish
			// and before the journal's Copied bit was synced.
			if err := validatePrivateVaultFile(destination); err != nil {
				return err
			}
			if err := c.verifyVault(destination); err != nil {
				return fmt.Errorf("verify resumed migration destination: %w", err)
			}
			if err := c.verifyPlaintext(archiveSource, destination, opener); err != nil {
				return fmt.Errorf("compare resumed migration destination: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		} else {
			if err := ensurePrivatePathDirectories(c.root, destination); err != nil {
				return err
			}
			if err := c.copyPlaintext(archiveSource, destination, opener); err != nil {
				return fmt.Errorf("encrypt legacy Desktop database %q: %w", artifact.Members[0].Source, err)
			}
			if err := c.migrationCheckpointLocked("artifact-copied"); err != nil {
				return err
			}
		}
		artifact.Copied = true
		if err := c.writeMigrationLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) verifyMigrationDestinationsLocked(journal *migrationJournal, opener *securedb.Opener, requirePlaintext bool) error {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		destination := filepath.Join(c.root, filepath.FromSlash(artifact.Destination))
		if err := validatePrivateVaultFile(destination); err != nil {
			return fmt.Errorf("validate migration destination %q: %w", artifact.Destination, err)
		}
		archiveAvailable := true
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			if member.ArchiveDeleted {
				archiveAvailable = false
				break
			}
			if _, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(member.Archive))); err != nil {
				archiveAvailable = false
				break
			}
		}
		if archiveAvailable {
			archiveSource := filepath.Join(c.root, filepath.FromSlash(artifact.Members[0].Archive))
			if err := c.verifyPlaintext(archiveSource, destination, opener); err != nil {
				return fmt.Errorf("verify migration destination %q against source archive: %w", artifact.Destination, err)
			}
			continue
		}
		if requirePlaintext {
			return fmt.Errorf("%w: source archive for %q is unavailable before cleanup", ErrMigrationState, artifact.Destination)
		}
		if err := c.verifyEncrypted(destination, opener); err != nil {
			return fmt.Errorf("verify encrypted migration destination %q: %w", artifact.Destination, err)
		}
	}
	return nil
}

func (c *Coordinator) resumeCleanupLocked(journal *migrationJournal) error {
	for index := range journal.Artifacts {
		artifact := &journal.Artifacts[index]
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			source := filepath.Join(c.root, filepath.FromSlash(member.Source))
			quarantine := filepath.Join(c.root, filepath.FromSlash(member.Quarantine))
			if !member.Quarantined {
				sourceInfo, sourceErr := os.Lstat(source)
				quarantineInfo, quarantineErr := os.Lstat(quarantine)
				switch {
				case sourceErr == nil && quarantineErr == nil:
					if !os.SameFile(sourceInfo, quarantineInfo) {
						return fmt.Errorf("%w: both source and quarantine exist for %q", ErrMigrationState, member.Source)
					}
					if err := os.Remove(source); err != nil {
						return err
					}
					if err := syncDirectory(filepath.Dir(source)); err != nil {
						return err
					}
				case sourceErr == nil && os.IsNotExist(quarantineErr):
					if err := verifyMemberFingerprint(source, member, member.Kind == "main"); err != nil {
						return err
					}
					if err := ensurePrivatePathDirectories(c.root, quarantine); err != nil {
						return err
					}
					if err := renameNoReplace(source, quarantine); err != nil {
						return fmt.Errorf("quarantine legacy Desktop database %q: %w", member.Source, err)
					}
					if err := syncDirectory(filepath.Dir(source)); err != nil {
						return err
					}
					if err := syncDirectory(filepath.Dir(quarantine)); err != nil {
						return err
					}
				case os.IsNotExist(sourceErr) && quarantineErr == nil:
					if err := verifyMemberFingerprint(quarantine, member, member.Kind == "main"); err != nil {
						return err
					}
				default:
					return fmt.Errorf("%w: pending source %q is missing", ErrMigrationState, member.Source)
				}
				if err := verifyMemberFingerprint(quarantine, member, member.Kind == "main"); err != nil {
					return err
				}
				if err := c.migrationCheckpointLocked("member-quarantined"); err != nil {
					return err
				}
				member.Quarantined = true
				if err := c.writeMigrationLocked(); err != nil {
					return err
				}
				if err := c.migrationCheckpointLocked("quarantine-journaled"); err != nil {
					return err
				}
			}
			if !member.Deleted {
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					return fmt.Errorf("%w: quarantined source %q reappeared", ErrMigrationState, member.Source)
				}
				if _, err := os.Lstat(quarantine); err == nil {
					if err := verifyMemberFingerprint(quarantine, member, member.Kind == "main"); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				}
				if err := os.Remove(quarantine); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("delete quarantined legacy database %q: %w", member.Source, err)
				}
				if err := syncDirectory(filepath.Dir(quarantine)); err != nil {
					return err
				}
				member.Deleted = true
				if err := c.writeMigrationLocked(); err != nil {
					return err
				}
			}
			if err := c.migrationCheckpointLocked("member-deleted"); err != nil {
				return err
			}
			if !member.ArchiveDeleted {
				archive := filepath.Join(c.root, filepath.FromSlash(member.Archive))
				if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("delete legacy migration work copy %q: %w", member.Archive, err)
				}
				if err := syncDirectory(filepath.Dir(archive)); err != nil {
					return err
				}
				if err := c.migrationCheckpointLocked("archive-deleted"); err != nil {
					return err
				}
				member.ArchiveDeleted = true
				if err := c.writeMigrationLocked(); err != nil {
					return err
				}
			}
		}
	}
	legacySnapshots := filepath.Join(c.root, "snapshots")
	if err := removeEmptyPrivateDirectory(legacySnapshots); err != nil {
		return err
	}
	workLeaf := filepath.Join(c.root, legacyWorkDir, journal.MigrationID)
	if err := removeEmptyDirectoryTree(workLeaf, filepath.Join(c.root, legacyWorkDir)); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) writeMigrationLocked() error {
	return writeMigrationJournalAtomic(filepath.Join(c.root, migrationFileName), c.migration)
}

func (c *Coordinator) refreshMigrationStateLocked() error {
	loaded, manifestErr := readManifest(c.manifestPath)
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return manifestErr
	}
	pending, journalErr := readMigrationJournal(filepath.Join(c.root, migrationFileName))
	if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
		return journalErr
	}
	if c.migration != nil && pending == nil {
		return fmt.Errorf("%w: durable migration journal disappeared", ErrMigrationState)
	}
	if c.manifest != nil && loaded == nil {
		return fmt.Errorf("%w: durable Desktop account manifest disappeared", ErrMigrationState)
	}
	if pending != nil {
		if err := pending.matchPublishedManifest(loaded); err != nil {
			return err
		}
		if err := pending.validateFilesystem(c.root, loaded != nil); err != nil {
			return err
		}
		c.manifest = loaded
		c.migration = pending
		c.legacyMigration = true
		return nil
	}
	if loaded != nil {
		if err := rejectPlaintextArtifactsWithManifest(c.root); err != nil {
			return err
		}
		c.manifest = loaded
		c.migration = nil
		c.legacyMigration = false
		return nil
	}
	legacy, err := inspectLegacyPath(c.legacyPath)
	if err != nil {
		return err
	}
	if !legacy {
		if err := rejectOrphanLegacyArtifacts(c.root); err != nil {
			return err
		}
	}
	c.manifest = nil
	c.migration = nil
	c.legacyMigration = legacy
	return nil
}

func (c *Coordinator) validateLegacyCleanupLocked(journal *migrationJournal) error {
	for artifactIndex := range journal.Artifacts {
		artifact := &journal.Artifacts[artifactIndex]
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			for _, relative := range []string{member.Source, member.Archive, member.Quarantine} {
				if _, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
					return fmt.Errorf("%w: plaintext migration artifact %q remains after cleanup", ErrMigrationState, relative)
				}
			}
		}
	}
	return nil
}

func (c *Coordinator) migrationCheckpointLocked(name string) error {
	if c.migrationFailpoint == nil {
		return nil
	}
	return c.migrationFailpoint(name)
}

func discoverMigrationArtifacts(root, vaultID, migrationID string) ([]migrationArtifact, error) {
	allowed := map[string]bool{
		legacyDBFileName: true, legacyDBFileName + ".bak": true, "snapshots": true,
		migrationLockFileName: true,
	}
	for _, primary := range []string{legacyDBFileName, legacyDBFileName + ".bak"} {
		for _, suffix := range sqliteSidecarSuffixes() {
			allowed[primary+suffix] = true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return nil, fmt.Errorf("unrecognized legacy Desktop artifact %q", entry.Name())
		}
	}
	for _, primary := range []string{legacyDBFileName, legacyDBFileName + ".bak"} {
		if _, err := os.Lstat(filepath.Join(root, primary)); os.IsNotExist(err) {
			for _, suffix := range sqliteSidecarSuffixes() {
				if _, sidecarErr := os.Lstat(filepath.Join(root, primary+suffix)); sidecarErr == nil {
					return nil, fmt.Errorf("orphan legacy SQLite sidecar %q", primary+suffix)
				} else if !os.IsNotExist(sidecarErr) {
					return nil, sidecarErr
				}
			}
		} else if err != nil {
			return nil, err
		}
	}
	var artifacts []migrationArtifact
	appendArtifact := func(source, destination string) error {
		var members []migrationMember
		for _, candidate := range append([]string{""}, sqliteSidecarSuffixes()...) {
			memberSource := source + candidate
			memberPath := filepath.Join(root, filepath.FromSlash(memberSource))
			if candidate != "" {
				if _, err := os.Lstat(memberPath); os.IsNotExist(err) {
					continue
				} else if err != nil {
					return err
				}
			}
			fingerprint, size, err := fingerprintRegularFile(memberPath, candidate == "")
			if err != nil {
				return err
			}
			kind := "main"
			if candidate != "" {
				kind = strings.TrimPrefix(candidate, "-")
			}
			members = append(members, migrationMember{
				Kind: kind, Source: memberSource,
				Archive:    filepath.ToSlash(filepath.Join(legacyWorkDir, migrationID, filepath.FromSlash(memberSource))),
				Quarantine: filepath.ToSlash(filepath.Join(legacyQuarantineDir, migrationID, filepath.FromSlash(memberSource))),
				SHA256:     fingerprint, Size: size,
			})
		}
		artifacts = append(artifacts, migrationArtifact{Destination: destination, Members: members})
		return nil
	}
	activeDestination := filepath.ToSlash(filepath.Join("vaults", vaultID, vaultDBFileName))
	if err := appendArtifact(legacyDBFileName, activeDestination); err != nil {
		return nil, err
	}
	backupPath := filepath.Join(root, legacyDBFileName+".bak")
	if _, err := os.Lstat(backupPath); err == nil {
		backupDestination := filepath.ToSlash(filepath.Join("vaults", vaultID, "snapshots", "legacy-backup.db"))
		if err := appendArtifact(legacyDBFileName+".bak", backupDestination); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	snapshotDir := filepath.Join(root, "snapshots")
	if snapshotInfo, snapshotErr := os.Lstat(snapshotDir); snapshotErr == nil {
		if snapshotInfo.Mode()&os.ModeSymlink != 0 || !snapshotInfo.IsDir() {
			return nil, errors.New("legacy snapshots path must be a real directory")
		}
	} else if !os.IsNotExist(snapshotErr) {
		return nil, snapshotErr
	}
	snapshotEntries, err := os.ReadDir(snapshotDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read legacy snapshots: %w", err)
	}
	if err == nil {
		sort.Slice(snapshotEntries, func(i, j int) bool { return snapshotEntries[i].Name() < snapshotEntries[j].Name() })
		primaryNames := make(map[string]bool)
		for _, entry := range snapshotEntries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && filepath.Ext(entry.Name()) == ".db" {
				primaryNames[entry.Name()] = true
			}
		}
		for _, entry := range snapshotEntries {
			name := entry.Name()
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || filepath.Base(name) != name {
				return nil, fmt.Errorf("unrecognized legacy snapshot artifact %q", name)
			}
			if !validLegacySnapshotName(name) {
				recognizedSidecar := false
				for _, suffix := range sqliteSidecarSuffixes() {
					if strings.HasSuffix(name, suffix) && primaryNames[strings.TrimSuffix(name, suffix)] {
						recognizedSidecar = true
						break
					}
				}
				if recognizedSidecar {
					continue
				}
				return nil, fmt.Errorf("unrecognized legacy snapshot artifact %q", name)
			}
			if name == "legacy-backup.db" && len(artifacts) > 1 && len(artifacts[1].Members) > 0 && artifacts[1].Members[0].Source == legacyDBFileName+".bak" {
				return nil, errors.New("legacy backup destination conflicts with an existing snapshot")
			}
			source := filepath.ToSlash(filepath.Join("snapshots", name))
			destination := filepath.ToSlash(filepath.Join("vaults", vaultID, "snapshots", name))
			if err := appendArtifact(source, destination); err != nil {
				return nil, err
			}
		}
	}
	return artifacts, nil
}

func fingerprintRegularPlaintext(path string) (string, int64, error) {
	return fingerprintRegularFile(path, true)
}

func fingerprintRegularFile(path string, requirePlaintextHeader bool) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, fmt.Errorf("inspect migration source %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("migration source %q must be a regular file", path)
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", 0, fmt.Errorf("migration source %q changed while opening", path)
	}
	if requirePlaintextHeader {
		header := make([]byte, len(plaintextSQLiteHeader))
		if _, err := io.ReadFull(file, header); err != nil || !bytes.Equal(header, []byte(plaintextSQLiteHeader)) {
			clear(header)
			return "", 0, fmt.Errorf("migration source %q is not a recognized plaintext SQLite database", path)
		}
		clear(header)
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", 0, err
		}
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return "", 0, fmt.Errorf("migration source %q changed while hashing", path)
	}
	return hex.EncodeToString(hash.Sum(nil)), after.Size(), nil
}

func verifyMemberFingerprint(path string, member *migrationMember, requirePlaintextHeader bool) error {
	digest, size, err := fingerprintRegularFile(path, requirePlaintextHeader)
	if err != nil {
		return err
	}
	if size != member.Size || digest != member.SHA256 {
		return fmt.Errorf("%w: migration source %q changed", ErrMigrationState, member.Source)
	}
	return nil
}

func sqliteSidecarSuffixes() []string { return []string{"-wal", "-shm", "-journal"} }

func copyRegularFileDurable(source, destination string) error {
	sourceFile, err := openRegularNoFollow(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	temporary, err := createPrivateTemp(filepath.Dir(destination), ".legacy-archive.tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := io.Copy(temporary, sourceFile); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := renameNoReplace(temporaryPath, destination); err != nil {
		return err
	}
	committed = true
	return syncDirectory(filepath.Dir(destination))
}

func ensurePrivatePathDirectories(root, filePath string) error {
	relative, err := filepath.Rel(root, filepath.Dir(filePath))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("private migration path escaped the Desktop data root")
	}
	current := root
	if relative == "." {
		return ensurePrivateDirectory(current)
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("private migration directory component is invalid")
		}
		current = filepath.Join(current, component)
		if err := ensurePrivateDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func removeEmptyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("private cleanup path %q is not a real directory", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove empty private directory %q: %w", path, err)
	}
	return syncDirectory(filepath.Dir(path))
}

func removeEmptyDirectoryTree(leaf, base string) error {
	for _, child := range []string{"snapshots"} {
		if err := removeEmptyPrivateDirectory(filepath.Join(leaf, child)); err != nil {
			return err
		}
	}
	if err := removeEmptyPrivateDirectory(leaf); err != nil {
		return err
	}
	return removeEmptyPrivateDirectory(base)
}

func (j *migrationJournal) validate() error {
	if j == nil || j.Version != migrationVersion || !identifierPattern.MatchString(j.MigrationID) {
		return fmt.Errorf("%w: invalid migration header", ErrMigrationState)
	}
	switch j.Phase {
	case migrationPhaseCopying, migrationPhasePublished, migrationPhaseCleanup, migrationPhaseDelivery:
	default:
		return fmt.Errorf("%w: invalid phase %q", ErrMigrationState, j.Phase)
	}
	if err := j.Manifest.validate(); err != nil {
		return err
	}
	if err := validateEnvelope(j.RecoveryDeliveryEnvelope, keyenvelope.KindPassword); err != nil {
		return fmt.Errorf("validate recovery delivery envelope: %w", err)
	}
	if len(j.Artifacts) == 0 || len(j.Artifacts[0].Members) == 0 || j.Artifacts[0].Members[0].Source != legacyDBFileName {
		return fmt.Errorf("%w: active legacy database must be the first artifact", ErrMigrationState)
	}
	seenSource := map[string]bool{}
	seenArchive := map[string]bool{}
	seenDestination := map[string]bool{}
	seenQuarantine := map[string]bool{}
	for index := range j.Artifacts {
		a := &j.Artifacts[index]
		if !validRelativeMigrationPath(a.Destination) || seenDestination[a.Destination] || len(a.Members) == 0 {
			return fmt.Errorf("%w: duplicate migration artifact path", ErrMigrationState)
		}
		seenDestination[a.Destination] = true
		primarySource := a.Members[0].Source
		expectedDestination, err := expectedMigrationDestination(j.Manifest.VaultID, primarySource, index)
		if err != nil || a.Destination != expectedDestination {
			return fmt.Errorf("%w: artifact source/destination mapping is invalid", ErrMigrationState)
		}
		vaultPrefix := filepath.ToSlash(filepath.Join("vaults", j.Manifest.VaultID)) + "/"
		if !strings.HasPrefix(a.Destination, vaultPrefix) {
			return fmt.Errorf("%w: artifact escaped its migration directory", ErrMigrationState)
		}
		seenKinds := map[string]bool{}
		for memberIndex := range a.Members {
			member := &a.Members[memberIndex]
			for name, value := range map[string]string{"source": member.Source, "archive": member.Archive, "quarantine": member.Quarantine} {
				if !validRelativeMigrationPath(value) {
					return fmt.Errorf("%w: artifact member %s path is invalid", ErrMigrationState, name)
				}
			}
			if seenSource[member.Source] || seenArchive[member.Archive] || seenQuarantine[member.Quarantine] {
				return fmt.Errorf("%w: duplicate migration member path", ErrMigrationState)
			}
			seenSource[member.Source], seenArchive[member.Archive], seenQuarantine[member.Quarantine] = true, true, true
			if memberIndex == 0 && member.Kind != "main" || memberIndex > 0 && member.Kind == "main" {
				return fmt.Errorf("%w: invalid SQLite tuple member order", ErrMigrationState)
			}
			if member.Kind != "main" && member.Kind != "wal" && member.Kind != "shm" && member.Kind != "journal" {
				return fmt.Errorf("%w: invalid SQLite tuple member kind", ErrMigrationState)
			}
			if seenKinds[member.Kind] {
				return fmt.Errorf("%w: duplicate SQLite tuple member kind", ErrMigrationState)
			}
			seenKinds[member.Kind] = true
			expectedSource := primarySource
			if member.Kind != "main" {
				expectedSource += "-" + member.Kind
			}
			expectedArchive := filepath.ToSlash(filepath.Join(legacyWorkDir, j.MigrationID, filepath.FromSlash(expectedSource)))
			expectedQuarantine := filepath.ToSlash(filepath.Join(legacyQuarantineDir, j.MigrationID, filepath.FromSlash(expectedSource)))
			if member.Source != expectedSource || member.Archive != expectedArchive || member.Quarantine != expectedQuarantine {
				return fmt.Errorf("%w: SQLite tuple member mapping is invalid", ErrMigrationState)
			}
			if len(member.SHA256) != sha256.Size*2 {
				return fmt.Errorf("%w: invalid source fingerprint", ErrMigrationState)
			}
			minimum := int64(0)
			if member.Kind == "main" {
				minimum = int64(len(plaintextSQLiteHeader))
			}
			if _, err := hex.DecodeString(member.SHA256); err != nil || member.Size < minimum {
				return fmt.Errorf("%w: invalid source fingerprint", ErrMigrationState)
			}
			if a.Copied && !member.Archived ||
				member.Quarantined && (!member.Archived || !a.Copied) ||
				member.Deleted && !member.Quarantined ||
				member.ArchiveDeleted && (!member.Archived || !member.Deleted || !a.Copied) {
				return fmt.Errorf("%w: invalid migration member state", ErrMigrationState)
			}
			archivePrefix := filepath.ToSlash(filepath.Join(legacyWorkDir, j.MigrationID)) + "/"
			quarantinePrefix := filepath.ToSlash(filepath.Join(legacyQuarantineDir, j.MigrationID)) + "/"
			if !strings.HasPrefix(member.Archive, archivePrefix) || !strings.HasPrefix(member.Quarantine, quarantinePrefix) {
				return fmt.Errorf("%w: member escaped its migration directory", ErrMigrationState)
			}
		}
		if seenKinds["shm"] && !seenKinds["wal"] || seenKinds["wal"] && seenKinds["journal"] {
			return fmt.Errorf("%w: incompatible SQLite sidecar tuple", ErrMigrationState)
		}
	}
	if j.Phase != migrationPhaseCopying {
		for _, a := range j.Artifacts {
			if !a.Copied {
				return fmt.Errorf("%w: published migration has an incomplete copy", ErrMigrationState)
			}
		}
	}
	if j.Phase == migrationPhaseDelivery {
		for _, a := range j.Artifacts {
			for _, member := range a.Members {
				if !member.Deleted || !member.ArchiveDeleted {
					return fmt.Errorf("%w: recovery delivery began before plaintext cleanup", ErrMigrationState)
				}
			}
		}
	}
	return nil
}

func expectedMigrationDestination(vaultID, source string, index int) (string, error) {
	vaultRoot := filepath.ToSlash(filepath.Join("vaults", vaultID))
	switch {
	case source == legacyDBFileName && index == 0:
		return vaultRoot + "/" + vaultDBFileName, nil
	case source == legacyDBFileName+".bak" && index > 0:
		return vaultRoot + "/snapshots/legacy-backup.db", nil
	case strings.HasPrefix(source, "snapshots/") && index > 0:
		name := strings.TrimPrefix(source, "snapshots/")
		if !validLegacySnapshotName(name) {
			return "", ErrMigrationState
		}
		return vaultRoot + "/snapshots/" + name, nil
	default:
		return "", ErrMigrationState
	}
}

func validLegacySnapshotName(name string) bool {
	return name != "" && name == filepath.Base(name) &&
		!strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..") &&
		strings.HasSuffix(name, ".db")
}

func validRelativeMigrationPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func (j *migrationJournal) matchPublishedManifest(published *manifest) error {
	if err := j.validate(); err != nil {
		return err
	}
	if published == nil {
		if j.Phase != migrationPhaseCopying {
			return fmt.Errorf("%w: published manifest is missing", ErrMigrationState)
		}
		return nil
	}
	left, _ := json.Marshal(j.Manifest)
	right, _ := json.Marshal(published)
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%w: journal and published manifest differ", ErrMigrationState)
	}
	return nil
}

func (j *migrationJournal) validateFilesystem(root string, manifestPublished bool) error {
	if err := j.validate(); err != nil {
		return err
	}
	for index := range j.Artifacts {
		a := &j.Artifacts[index]
		destination := filepath.Join(root, filepath.FromSlash(a.Destination))
		if a.Copied {
			if info, err := os.Lstat(destination); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("%w: copied destination %q is missing or unsafe", ErrMigrationState, a.Destination)
			}
		} else if _, err := os.Lstat(destination); err != nil && !os.IsNotExist(err) {
			return err
		}
		for memberIndex := range a.Members {
			member := &a.Members[memberIndex]
			source := filepath.Join(root, filepath.FromSlash(member.Source))
			archive := filepath.Join(root, filepath.FromSlash(member.Archive))
			quarantine := filepath.Join(root, filepath.FromSlash(member.Quarantine))
			if member.Archived && !member.ArchiveDeleted {
				if info, err := os.Lstat(archive); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					if !(os.IsNotExist(err) && a.Copied && j.Phase == migrationPhaseCleanup) {
						return fmt.Errorf("%w: archived source %q is missing or unsafe", ErrMigrationState, member.Archive)
					}
				}
				if _, err := os.Lstat(archive); err == nil {
					if err := verifyMemberFingerprint(archive, member, false); err != nil {
						return err
					}
				}
			}
			if member.ArchiveDeleted {
				if _, err := os.Lstat(archive); !os.IsNotExist(err) {
					return fmt.Errorf("%w: deleted work archive %q reappeared", ErrMigrationState, member.Archive)
				}
			}
			if member.Deleted {
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					return fmt.Errorf("%w: deleted source %q reappeared", ErrMigrationState, member.Source)
				}
				if _, err := os.Lstat(quarantine); !os.IsNotExist(err) {
					return fmt.Errorf("%w: deleted quarantine %q reappeared", ErrMigrationState, member.Quarantine)
				}
			} else if member.Quarantined {
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					return fmt.Errorf("%w: quarantined source %q reappeared", ErrMigrationState, member.Source)
				}
				if _, err := os.Lstat(quarantine); err != nil && !os.IsNotExist(err) {
					return err
				}
				if _, err := os.Lstat(quarantine); err == nil {
					if err := verifyMemberFingerprint(quarantine, member, member.Kind == "main"); err != nil {
						return err
					}
				}
			} else {
				if _, err := os.Lstat(source); err == nil {
					if err := verifyMemberFingerprint(source, member, member.Kind == "main"); err != nil {
						return err
					}
				} else {
					if _, quarantineErr := os.Lstat(quarantine); quarantineErr != nil {
						return fmt.Errorf("%w: pending source %q is missing", ErrMigrationState, member.Source)
					}
					if err := verifyMemberFingerprint(quarantine, member, member.Kind == "main"); err != nil {
						return err
					}
				}
			}
		}
	}
	if manifestPublished && j.Phase == migrationPhaseCopying {
		// Crash after atomic manifest publication and before the journal phase
		// update is a supported recovery boundary.
		return validateKnownMigrationTree(root, j, manifestPublished)
	}
	return validateKnownMigrationTree(root, j, manifestPublished)
}

func validateKnownMigrationTree(root string, journal *migrationJournal, manifestPublished bool) error {
	allowedFiles := map[string]bool{migrationFileName: true, migrationLockFileName: true}
	trustedSnapshotLocks := map[string]bool{}
	if manifestPublished {
		allowedFiles[manifestFileName] = true
	}
	allowedDirectories := map[string]bool{".": true}
	allowParents := func(relative string) {
		relative = filepath.ToSlash(relative)
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
		for parent != "." {
			allowedDirectories[parent] = true
			parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent)))
		}
	}
	allowFile := func(relative string) {
		relative = filepath.ToSlash(relative)
		allowedFiles[relative] = true
		allowParents(relative)
	}
	for artifactIndex := range journal.Artifacts {
		artifact := &journal.Artifacts[artifactIndex]
		allowFile(artifact.Destination)
		destinationParent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(artifact.Destination)))
		if filepath.Base(filepath.FromSlash(destinationParent)) == "snapshots" {
			lockRelative := filepath.ToSlash(filepath.Join(filepath.FromSlash(destinationParent), database.SnapshotTransactionLockFileName))
			trustedSnapshotLocks[lockRelative] = true
			allowParents(lockRelative)
		}
		if artifactIndex == 0 {
			for _, suffix := range sqliteSidecarSuffixes() {
				allowFile(artifact.Destination + suffix)
			}
		}
		stageFiles, stageDirectory, err := validateExpectedSecureDBStage(root, artifact.Destination)
		if err != nil {
			return err
		}
		if stageDirectory != "" {
			allowedDirectories[stageDirectory] = true
			allowParents(stageDirectory + "/placeholder")
			for _, stageFile := range stageFiles {
				allowedFiles[stageDirectory+"/"+stageFile] = true
			}
		}
		for memberIndex := range artifact.Members {
			member := &artifact.Members[memberIndex]
			allowParents(member.Quarantine)
			if journal.Phase != migrationPhaseDelivery {
				allowParents(member.Source)
				allowParents(member.Archive)
			}
			if !member.Quarantined && !member.Deleted {
				allowFile(member.Source)
			}
			if !member.Deleted && journal.Phase != migrationPhaseDelivery {
				allowFile(member.Quarantine)
			}
			if !member.ArchiveDeleted && journal.Phase != migrationPhaseDelivery {
				allowFile(member.Archive)
			}
		}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link %q exists in migration tree", ErrMigrationState, relative)
		}
		if entry.IsDir() {
			if !allowedDirectories[relative] {
				return fmt.Errorf("%w: unknown directory %q exists in migration tree", ErrMigrationState, relative)
			}
			return nil
		}
		if trustedSnapshotLocks[relative] {
			if err := database.ValidateSnapshotTransactionLock(path); err != nil {
				return fmt.Errorf("%w: destination snapshot coordination file %q is unsafe: %v", ErrMigrationState, relative, err)
			}
			return nil
		}
		if !entry.Type().IsRegular() || !allowedFiles[relative] {
			if !entry.Type().IsRegular() || !isAllowedMigrationTemp(relative, journal) {
				return fmt.Errorf("%w: unknown file %q exists in migration tree", ErrMigrationState, relative)
			}
			info, err := entry.Info()
			if err != nil || hasInsecurePermissions(info.Mode()) {
				return fmt.Errorf("%w: migration temporary file %q is unsafe", ErrMigrationState, relative)
			}
		}
		return nil
	})
}

func isAllowedMigrationTemp(relative string, journal *migrationJournal) bool {
	if !strings.Contains(relative, "/") &&
		(strings.HasPrefix(relative, ".desktop-migration.tmp-") || strings.HasPrefix(relative, ".desktop-account.tmp-")) {
		return true
	}
	if !strings.HasPrefix(filepath.Base(filepath.FromSlash(relative)), ".legacy-archive.tmp-") {
		return false
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	for artifactIndex := range journal.Artifacts {
		for memberIndex := range journal.Artifacts[artifactIndex].Members {
			archiveParent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(journal.Artifacts[artifactIndex].Members[memberIndex].Archive)))
			if parent == archiveParent {
				return true
			}
		}
	}
	return false
}

func cleanupRootMigrationTemps(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".desktop-migration.tmp-") && !strings.HasPrefix(name, ".desktop-account.tmp-") {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || hasInsecurePermissions(info.Mode()) {
			return fmt.Errorf("%w: migration temporary file %q is unsafe", ErrMigrationState, name)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}

func cleanupJournalMigrationTemps(root string, journal *migrationJournal) error {
	seen := map[string]bool{}
	for artifactIndex := range journal.Artifacts {
		for memberIndex := range journal.Artifacts[artifactIndex].Members {
			directory := filepath.Join(root, filepath.Dir(filepath.FromSlash(journal.Artifacts[artifactIndex].Members[memberIndex].Archive)))
			if seen[directory] {
				continue
			}
			seen[directory] = true
			entries, err := os.ReadDir(directory)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".legacy-archive.tmp-") {
					continue
				}
				path := filepath.Join(directory, entry.Name())
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || hasInsecurePermissions(info.Mode()) {
					return fmt.Errorf("%w: legacy archive temporary file is unsafe", ErrMigrationState)
				}
				if err := os.Remove(path); err != nil {
					return err
				}
			}
			if err := syncDirectory(directory); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateExpectedSecureDBStage(root, destinationRelative string) ([]string, string, error) {
	destination := filepath.Join(root, filepath.FromSlash(destinationRelative))
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	stageName := ".omni-cipher-copy-" + hex.EncodeToString(digest[:12])
	stagePath := filepath.Join(filepath.Dir(destination), stageName)
	info, err := os.Lstat(stagePath)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || hasInsecurePermissions(info.Mode()) {
		return nil, "", fmt.Errorf("%w: secure database staging path is unsafe", ErrMigrationState)
	}
	allowed := map[string]bool{
		".omni-securedb-copy-v1": false,
		"plaintext.db":           false,
		"plaintext.db-wal":       false,
		"plaintext.db-shm":       false,
		"plaintext.db-journal":   false,
		"encrypted.db":           false,
		"encrypted.db-wal":       false,
		"encrypted.db-shm":       false,
		"encrypted.db-journal":   false,
	}
	entries, err := os.ReadDir(stagePath)
	if err != nil {
		return nil, "", err
	}
	if len(entries) == 0 {
		relative, relErr := filepath.Rel(root, stagePath)
		if relErr != nil || !validRelativeMigrationPath(filepath.ToSlash(relative)) {
			return nil, "", fmt.Errorf("%w: secure database stage escaped the data root", ErrMigrationState)
		}
		return nil, filepath.ToSlash(relative), nil
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return nil, "", fmt.Errorf("%w: secure database stage contains unknown artifact %q", ErrMigrationState, entry.Name())
		}
		entryInfo, err := os.Lstat(filepath.Join(stagePath, entry.Name()))
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 || !entryInfo.Mode().IsRegular() || hasInsecurePermissions(entryInfo.Mode()) {
			return nil, "", fmt.Errorf("%w: secure database stage artifact %q is unsafe", ErrMigrationState, entry.Name())
		}
		allowed[entry.Name()] = true
		actual = append(actual, entry.Name())
	}
	if !allowed[".omni-securedb-copy-v1"] {
		return nil, "", fmt.Errorf("%w: secure database stage marker is missing", ErrMigrationState)
	}
	markerPath := filepath.Join(stagePath, ".omni-securedb-copy-v1")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return nil, "", err
	}
	marker, err := readStrictPrivateFile(markerPath, 1024, "secure database stage marker")
	if err != nil {
		// securedb can safely recover a crash immediately after creating an
		// empty marker, provided no database artifact exists beside it.
		if len(entries) != 1 || markerInfo.Size() != 0 {
			return nil, "", err
		}
		marker = []byte{}
	}
	defer clear(marker)
	expectedMarker := "omni-money securedb copy v1\n" + hex.EncodeToString(digest[:]) + "\n"
	if string(marker) != expectedMarker {
		if len(entries) != 1 || len(marker) >= len(expectedMarker) || !bytes.HasPrefix([]byte(expectedMarker), marker) {
			return nil, "", fmt.Errorf("%w: secure database stage marker does not match its destination", ErrMigrationState)
		}
	}
	relative, err := filepath.Rel(root, stagePath)
	if err != nil || !validRelativeMigrationPath(filepath.ToSlash(relative)) {
		return nil, "", fmt.Errorf("%w: secure database stage escaped the data root", ErrMigrationState)
	}
	return actual, filepath.ToSlash(relative), nil
}

func readMigrationJournal(path string) (*migrationJournal, error) {
	content, err := readStrictPrivateFile(path, maxMigrationBytes, "Desktop migration journal")
	if err != nil {
		return nil, err
	}
	defer clear(content)
	if err := rejectDuplicateJSONFields(content); err != nil {
		return nil, fmt.Errorf("decode Desktop migration journal: %w", err)
	}
	if err := requireExactMigrationFields(content); err != nil {
		return nil, fmt.Errorf("decode Desktop migration journal: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var journal migrationJournal
	if err := decoder.Decode(&journal); err != nil {
		return nil, fmt.Errorf("decode Desktop migration journal: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode Desktop migration journal: %w", err)
	}
	if err := journal.validate(); err != nil {
		return nil, err
	}
	return &journal, nil
}

func readStrictPrivateFile(path string, limit int64, label string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || hasInsecurePermissions(info.Mode()) {
		return nil, fmt.Errorf("%s must be an owner-only regular file", label)
	}
	if info.Size() <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("%s size is invalid", label)
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%s changed while opening", label)
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(content) == 0 || int64(len(content)) > limit {
		clear(content)
		return nil, fmt.Errorf("read %s: size or I/O failure", label)
	}
	return content, nil
}

func requireExactMigrationFields(content []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(content, &root); err != nil {
		return err
	}
	if err := requireExactObject(root, []string{"version", "migration_id", "phase", "manifest", "recovery_delivery_envelope", "artifacts"}); err != nil {
		return err
	}
	if err := requireExactManifestFields(root["manifest"]); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := requireExactEnvelopeFields(root["recovery_delivery_envelope"], true); err != nil {
		return fmt.Errorf("recovery_delivery_envelope: %w", err)
	}
	var artifacts []map[string]json.RawMessage
	if err := json.Unmarshal(root["artifacts"], &artifacts); err != nil {
		return err
	}
	for index, artifact := range artifacts {
		if err := requireExactObject(artifact, []string{"destination", "copied", "members"}); err != nil {
			return fmt.Errorf("artifacts[%d]: %w", index, err)
		}
		var members []map[string]json.RawMessage
		if err := json.Unmarshal(artifact["members"], &members); err != nil {
			return fmt.Errorf("artifacts[%d].members: %w", index, err)
		}
		for memberIndex, member := range members {
			if err := requireExactObject(member, []string{"kind", "source", "archive", "quarantine", "sha256", "size", "archived", "quarantined", "deleted", "archive_deleted"}); err != nil {
				return fmt.Errorf("artifacts[%d].members[%d]: %w", index, memberIndex, err)
			}
		}
	}
	return nil
}

func writeMigrationJournalAtomic(path string, journal *migrationJournal) error {
	if err := journal.validate(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	defer clear(content)
	if len(content) > maxMigrationBytes {
		return errors.New("Desktop migration journal exceeds its size limit")
	}
	directory := filepath.Dir(path)
	temporary, err := createPrivateTemp(directory, ".desktop-migration.tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return syncDirectory(directory)
}
