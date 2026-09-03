// Package desktopaccount owns the lifecycle of the single local Desktop
// account and its encrypted financial vault.
//
// The coordinator always starts locked. It persists only public identifiers
// and authenticated key envelopes; plaintext passwords, recovery secrets, and
// vault keys are never written to disk. Callers remain responsible for
// clearing password and recovery-secret arguments after each call.
package desktopaccount

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/passwordpolicy"
	"omni_money/backend/securedb"
)

const (
	RoleAdmin             = "admin"
	manifestFileName      = "desktop-account.json"
	migrationFileName     = "desktop-migration.json"
	legacyDBFileName      = "omni_money.db"
	vaultDBFileName       = "omni_money.db"
	plaintextSQLiteHeader = "SQLite format 3\x00"
)

var (
	ErrClosed                  = errors.New("desktop account coordinator is closed")
	ErrNotConfigured           = errors.New("desktop account is not configured")
	ErrAlreadyConfigured       = errors.New("desktop account is already configured")
	ErrLocked                  = errors.New("desktop account is locked")
	ErrAlreadyUnlocked         = errors.New("desktop account is already unlocked")
	ErrLeaseReleased           = errors.New("desktop account service lease is released")
	ErrLegacyMigrationRequired = errors.New("legacy plaintext Desktop database requires explicit migration")
	ErrMigrationPending        = errors.New("legacy Desktop migration recovery acknowledgment is pending")
	ErrInvalidPassword         = passwordpolicy.ErrInvalid
)

// Status contains only non-secret state and is safe to expose to the Desktop
// frontend. Role is "admin" for the configured single account and empty before
// setup.
type Status struct {
	Configured              bool   `json:"configured"`
	Unlocked                bool   `json:"unlocked"`
	LegacyMigrationRequired bool   `json:"legacy_migration_required"`
	Role                    string `json:"role"`
}

type vaultOpener func(path string, key securedb.RawKey) (*database.Instance, error)
type vaultVerifier func(path string) error
type plaintextCopier func(sourcePath, destinationPath string, opener *securedb.Opener) error
type plaintextVerifier func(sourcePath, destinationPath string, opener *securedb.Opener) error
type encryptedVerifier func(path string, opener *securedb.Opener) error

// Coordinator serializes setup, unlock, recovery, and credential rotation.
// It deliberately does not retain the plaintext vault key: the live encrypted
// database opener is the only long-lived owner of a key while unlocked.
type Coordinator struct {
	root               string
	manifestPath       string
	legacyPath         string
	openFresh          vaultOpener
	openExisting       vaultOpener
	verifyVault        vaultVerifier
	copyPlaintext      plaintextCopier
	verifyPlaintext    plaintextVerifier
	verifyEncrypted    encryptedVerifier
	migrationFailpoint func(string) error

	mu              sync.Mutex
	cond            *sync.Cond
	manifest        *manifest
	migration       *migrationJournal
	instance        *database.Instance
	legacyMigration bool
	active          int
	draining        bool
	closed          bool
	generation      uint64
}

// ServiceLease keeps the unlocked database alive for one in-flight operation.
// Callers must Release it, normally with defer, after using Core.
type ServiceLease struct {
	owner      *Coordinator
	service    *core.Service
	generation uint64
	mu         sync.Mutex
	released   bool
}

// New prepares a private Desktop data root and loads a manifest without
// opening its vault. If the historical root/omni_money.db exists without a
// manifest, Status reports that an explicit legacy migration is required.
func New(root string) (*Coordinator, error) {
	return newCoordinator(
		root,
		database.OpenEncryptedInstance,
		database.OpenExistingEncryptedInstance,
		securedb.RequireEncryptedHeader,
	)
}

func newCoordinator(root string, freshOpener, existingOpener vaultOpener, verifier vaultVerifier) (*Coordinator, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("Desktop account data root is required")
	}
	if freshOpener == nil || existingOpener == nil || verifier == nil {
		return nil, errors.New("Desktop account vault dependencies are required")
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve Desktop account data root: %w", err)
	}
	if filepath.Dir(absolute) == absolute {
		return nil, errors.New("Desktop account data root cannot be a filesystem root")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if absoluteHome, absErr := filepath.Abs(filepath.Clean(home)); absErr == nil && absolute == absoluteHome {
			return nil, errors.New("Desktop account data root cannot be the user home directory")
		}
	}
	if err := ensurePrivateDirectory(absolute); err != nil {
		return nil, err
	}

	c := &Coordinator{
		root:            absolute,
		manifestPath:    filepath.Join(absolute, manifestFileName),
		legacyPath:      filepath.Join(absolute, legacyDBFileName),
		openFresh:       freshOpener,
		openExisting:    existingOpener,
		verifyVault:     verifier,
		copyPlaintext:   defaultPlaintextCopier,
		verifyPlaintext: defaultPlaintextVerifier,
		verifyEncrypted: defaultEncryptedVerifier,
	}
	c.cond = sync.NewCond(&c.mu)

	loaded, err := readManifest(c.manifestPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	pending, pendingErr := readMigrationJournal(filepath.Join(absolute, migrationFileName))
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return nil, pendingErr
	}
	if pending != nil {
		if err := pending.matchPublishedManifest(loaded); err != nil {
			return nil, err
		}
		if err := pending.validateFilesystem(absolute, loaded != nil); err != nil {
			return nil, err
		}
		c.manifest = loaded
		c.migration = pending
		c.legacyMigration = true
		if loaded != nil {
			c.generation = 1
		}
		return c, nil
	}
	if loaded != nil {
		if err := rejectPlaintextArtifactsWithManifest(absolute); err != nil {
			return nil, err
		}
		c.manifest = loaded
		c.generation = 1
		return c, nil
	}
	legacy, err := inspectLegacyPath(c.legacyPath)
	if err != nil {
		return nil, err
	}
	if !legacy {
		if err := rejectOrphanLegacyArtifacts(absolute); err != nil {
			return nil, err
		}
	}
	c.legacyMigration = legacy
	return c, nil
}

// Status never opens the vault or performs password derivation.
func (c *Coordinator) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	configured := c.manifest != nil
	role := ""
	if configured {
		role = RoleAdmin
	}
	return Status{
		Configured:              configured,
		Unlocked:                c.migration == nil && c.instance != nil && !c.draining && !c.closed,
		LegacyMigrationRequired: c.legacyMigration,
		Role:                    role,
	}
}

// Setup creates the sole local admin account and a new SQLCipher vault. The
// returned raw 32-byte recovery secret is shown/exported once and must be
// cleared by the caller. Setup leaves the account unlocked on success.
func (c *Coordinator) Setup(password []byte) (recoverySecret []byte, err error) {
	passwordCopy, err := copyPassword(password)
	if err != nil {
		return nil, err
	}
	defer clear(passwordCopy)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireMutableLocked(); err != nil {
		return nil, err
	}
	if c.migration != nil {
		return nil, ErrMigrationPending
	}
	if c.manifest != nil {
		return nil, ErrAlreadyConfigured
	}
	if c.legacyMigration {
		return nil, ErrLegacyMigrationRequired
	}

	installationID, err := generateID()
	if err != nil {
		return nil, err
	}
	userID, err := generateID()
	if err != nil {
		return nil, err
	}
	vaultID, err := generateID()
	if err != nil {
		return nil, err
	}
	context := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	dek, err := keyenvelope.GenerateDEK()
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	recoverySecret, err = keyenvelope.GenerateRecoverySecret()
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			clear(recoverySecret)
			recoverySecret = nil
		}
	}()

	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, passwordCopy, context)
	if err != nil {
		return nil, err
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, recoverySecret, context)
	if err != nil {
		return nil, err
	}
	document := &manifest{
		Version:          manifestVersion,
		InstallationID:   installationID,
		UserID:           userID,
		VaultID:          vaultID,
		Role:             RoleAdmin,
		Generation:       1,
		PasswordEnvelope: passwordEnvelope,
		RecoveryEnvelope: recoveryEnvelope,
	}
	if err := document.validate(); err != nil {
		return nil, err
	}

	vaultPath := c.vaultPath(vaultID)
	if err := ensureFreshVaultPath(vaultPath); err != nil {
		return nil, err
	}
	key, err := securedb.NewRawKey(dek)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	instance, err := c.openFresh(vaultPath, key)
	if err != nil {
		cleanupFreshVault(vaultPath)
		return nil, fmt.Errorf("create encrypted Desktop vault: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = instance.Close()
			cleanupFreshVault(vaultPath)
		}
	}()
	if err := writeManifestAtomic(c.manifestPath, document); err != nil {
		return nil, err
	}
	committed = true
	c.manifest = document
	c.instance = instance
	c.generation++
	succeeded = true
	return recoverySecret, nil
}

// Unlock authenticates the password and opens the existing SQLCipher vault.
func (c *Coordinator) Unlock(password []byte) error {
	passwordCopy, err := copyPassword(password)
	if err != nil {
		return err
	}
	defer clear(passwordCopy)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireMutableLocked(); err != nil {
		return err
	}
	if c.migration != nil {
		return ErrMigrationPending
	}
	if c.manifest == nil {
		if c.legacyMigration {
			return ErrLegacyMigrationRequired
		}
		return ErrNotConfigured
	}
	if c.instance != nil {
		return ErrAlreadyUnlocked
	}
	dek, err := keyenvelope.UnwrapWithPassword(c.manifest.PasswordEnvelope, passwordCopy, c.manifest.context())
	if err != nil {
		clear(dek)
		return err
	}
	defer clear(dek)
	instance, err := c.openExistingVaultLocked(dek)
	if err != nil {
		return err
	}
	c.instance = instance
	c.generation++
	return nil
}

// Recover authenticates the current recovery secret, assigns a new password,
// rotates the recovery secret, and unlocks the vault. The old recovery secret
// becomes invalid only after the replacement manifest is durably published.
func (c *Coordinator) Recover(recoverySecret, newPassword []byte) (nextRecoverySecret []byte, err error) {
	if len(recoverySecret) != keyenvelope.RecoverySecretSize {
		return nil, keyenvelope.ErrAuthentication
	}
	recoveryCopy := append([]byte(nil), recoverySecret...)
	defer clear(recoveryCopy)
	passwordCopy, err := copyPassword(newPassword)
	if err != nil {
		return nil, err
	}
	defer clear(passwordCopy)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireMutableLocked(); err != nil {
		return nil, err
	}
	if c.migration != nil {
		return nil, ErrMigrationPending
	}
	if c.manifest == nil {
		return nil, ErrNotConfigured
	}
	if c.instance != nil {
		return nil, ErrAlreadyUnlocked
	}
	dek, err := keyenvelope.UnwrapWithRecovery(c.manifest.RecoveryEnvelope, recoveryCopy, c.manifest.context())
	if err != nil {
		clear(dek)
		return nil, err
	}
	defer clear(dek)
	instance, err := c.openExistingVaultLocked(dek)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = instance.Close()
		}
	}()

	nextRecoverySecret, err = keyenvelope.GenerateRecoverySecret()
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			clear(nextRecoverySecret)
			nextRecoverySecret = nil
		}
	}()
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, passwordCopy, c.manifest.context())
	if err != nil {
		return nil, err
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, nextRecoverySecret, c.manifest.context())
	if err != nil {
		return nil, err
	}
	replacement, err := c.manifest.withEnvelopes(passwordEnvelope, recoveryEnvelope)
	if err != nil {
		return nil, err
	}
	if err := writeManifestAtomic(c.manifestPath, replacement); err != nil {
		return nil, err
	}
	committed = true
	c.manifest = replacement
	c.instance = instance
	c.generation++
	succeeded = true
	return nextRecoverySecret, nil
}

// ChangePassword atomically replaces only the password envelope. The current
// password is required even while the account is unlocked.
func (c *Coordinator) ChangePassword(currentPassword, newPassword []byte) error {
	currentCopy, err := copyPassword(currentPassword)
	if err != nil {
		return err
	}
	defer clear(currentCopy)
	newCopy, err := copyPassword(newPassword)
	if err != nil {
		return err
	}
	defer clear(newCopy)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireUnlockedLocked(); err != nil {
		return err
	}
	dek, err := keyenvelope.UnwrapWithPassword(c.manifest.PasswordEnvelope, currentCopy, c.manifest.context())
	if err != nil {
		clear(dek)
		return err
	}
	defer clear(dek)
	replacementEnvelope, err := keyenvelope.WrapWithPassword(dek, newCopy, c.manifest.context())
	if err != nil {
		return err
	}
	replacement, err := c.manifest.withEnvelopes(replacementEnvelope, c.manifest.RecoveryEnvelope)
	if err != nil {
		return err
	}
	if err := writeManifestAtomic(c.manifestPath, replacement); err != nil {
		return err
	}
	c.manifest = replacement
	return nil
}

// RotateRecovery replaces the recovery envelope after re-authenticating the
// password. The caller must generate and save the candidate secret before
// calling: manifest publication is the single atomic commit point.
func (c *Coordinator) RotateRecovery(currentPassword, recoverySecret []byte) error {
	passwordCopy, err := copyPassword(currentPassword)
	if err != nil {
		return err
	}
	defer clear(passwordCopy)
	if len(recoverySecret) != keyenvelope.RecoverySecretSize {
		return keyenvelope.ErrInvalidRecoverySecret
	}
	recoveryCopy := append([]byte(nil), recoverySecret...)
	defer clear(recoveryCopy)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireUnlockedLocked(); err != nil {
		return err
	}
	dek, err := keyenvelope.UnwrapWithPassword(c.manifest.PasswordEnvelope, passwordCopy, c.manifest.context())
	if err != nil {
		clear(dek)
		return err
	}
	defer clear(dek)
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, recoveryCopy, c.manifest.context())
	if err != nil {
		return err
	}
	replacement, err := c.manifest.withEnvelopes(c.manifest.PasswordEnvelope, recoveryEnvelope)
	if err != nil {
		return err
	}
	if err := writeManifestAtomic(c.manifestPath, replacement); err != nil {
		return err
	}
	c.manifest = replacement
	return nil
}

// Service borrows a guarded business service. Lock and Close wait for all
// borrowed leases to be released before closing the database.
func (c *Coordinator) Service() (*ServiceLease, error) {
	if c == nil {
		return nil, ErrClosed
	}
	c.mu.Lock()
	if err := c.requireUnlockedLocked(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.active++
	lease := &ServiceLease{owner: c, generation: c.generation}
	c.mu.Unlock()
	service, err := core.NewGuardedService(c.instance, lease.live)
	if err != nil {
		lease.Release()
		return nil, err
	}
	lease.mu.Lock()
	lease.service = service
	lease.mu.Unlock()
	return lease, nil
}

// Core returns the guarded business service while the lease is live.
func (l *ServiceLease) Core() (*core.Service, error) {
	if l == nil {
		return nil, ErrLeaseReleased
	}
	l.mu.Lock()
	service := l.service
	released := l.released
	l.mu.Unlock()
	if released || service == nil || !l.live() {
		return nil, ErrLeaseReleased
	}
	return service, nil
}

// Release ends one in-flight operation. Repeated calls are safe.
func (l *ServiceLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	owner := l.owner
	l.owner = nil
	l.service = nil
	l.mu.Unlock()
	if owner != nil {
		owner.releaseOperation()
	}
}

func (l *ServiceLease) live() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.owner == nil {
		return false
	}
	c := l.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && !c.draining && c.instance != nil && c.generation == l.generation
}

// CreateSnapshot creates an encrypted snapshot while holding an internal
// operation lease, so Lock cannot close the database mid-copy.
func (c *Coordinator) CreateSnapshot() (string, error) {
	instance, release, err := c.borrowInstance()
	if err != nil {
		return "", err
	}
	defer release()
	return instance.CreateSnapshot("")
}

func (c *Coordinator) ListSnapshots() ([]string, error) {
	instance, release, err := c.borrowInstance()
	if err != nil {
		return nil, err
	}
	defer release()
	return instance.ListSnapshots("")
}

// RestoreSnapshot drains all current operations and prevents every previously
// issued Service lease from being reused across the restore boundary.
func (c *Coordinator) RestoreSnapshot(name string) error {
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	if err := c.requireUnlockedLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.draining = true
	for c.active > 0 {
		c.cond.Wait()
	}
	instance := c.instance
	c.generation++
	c.mu.Unlock()

	err := instance.RestoreSnapshot("", name)
	c.mu.Lock()
	if err != nil {
		// Keep draining true while detaching the instance. Status/Service and
		// concurrent snapshot calls therefore cannot observe an unlocked
		// coordinator in the gap before cleanup. Close outside c.mu, then
		// publish the locked state and wake waiters together.
		if c.instance == instance {
			c.instance = nil
			c.generation++
		}
		c.mu.Unlock()
		closeErr := instance.Close()
		c.mu.Lock()
		c.draining = false
		c.cond.Broadcast()
		c.mu.Unlock()
		return errors.Join(err, closeErr)
	}
	c.draining = false
	c.cond.Broadcast()
	c.mu.Unlock()
	return err
}

// Lock waits for in-flight Service/snapshot leases and destroys the live
// database opener's in-memory SQLCipher key. It is idempotent.
func (c *Coordinator) Lock() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	for c.draining {
		c.cond.Wait()
	}
	if c.migration != nil {
		c.mu.Unlock()
		return ErrMigrationPending
	}
	if c.instance == nil {
		c.mu.Unlock()
		return nil
	}
	c.draining = true
	for c.active > 0 {
		c.cond.Wait()
	}
	instance := c.instance
	c.instance = nil
	c.generation++
	c.mu.Unlock()

	err := instance.Close()
	c.mu.Lock()
	c.draining = false
	c.cond.Broadcast()
	c.mu.Unlock()
	return err
}

// Close permanently closes this coordinator. Repeated calls are safe.
func (c *Coordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	for c.draining {
		c.cond.Wait()
	}
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.instance == nil {
		c.mu.Unlock()
		return nil
	}
	c.draining = true
	for c.active > 0 {
		c.cond.Wait()
	}
	instance := c.instance
	c.instance = nil
	c.generation++
	c.mu.Unlock()

	err := instance.Close()
	c.mu.Lock()
	c.draining = false
	c.cond.Broadcast()
	c.mu.Unlock()
	return err
}

func (c *Coordinator) borrowInstance() (*database.Instance, func(), error) {
	if c == nil {
		return nil, nil, ErrClosed
	}
	c.mu.Lock()
	if err := c.requireUnlockedLocked(); err != nil {
		c.mu.Unlock()
		return nil, nil, err
	}
	c.active++
	instance := c.instance
	c.mu.Unlock()
	return instance, c.releaseOperation, nil
}

func (c *Coordinator) releaseOperation() {
	c.mu.Lock()
	if c.active > 0 {
		c.active--
	}
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *Coordinator) requireMutableLocked() error {
	if c == nil || c.closed {
		return ErrClosed
	}
	if c.draining {
		return ErrBusy
	}
	return nil
}

func (c *Coordinator) requireUnlockedLocked() error {
	if err := c.requireMutableLocked(); err != nil {
		return err
	}
	if c.migration != nil {
		return ErrMigrationPending
	}
	if c.manifest == nil {
		return ErrNotConfigured
	}
	if c.instance == nil {
		return ErrLocked
	}
	return nil
}

func (c *Coordinator) openExistingVaultLocked(dek []byte) (*database.Instance, error) {
	path := c.vaultPath(c.manifest.VaultID)
	if err := validatePrivateVaultFile(path); err != nil {
		return nil, err
	}
	if err := c.verifyVault(path); err != nil {
		return nil, fmt.Errorf("verify encrypted Desktop vault: %w", err)
	}
	key, err := securedb.NewRawKey(dek)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	instance, err := c.openExisting(path, key)
	if err != nil {
		return nil, fmt.Errorf("open encrypted Desktop vault: %w", err)
	}
	return instance, nil
}

func (c *Coordinator) vaultPath(vaultID string) string {
	return filepath.Join(c.root, "vaults", vaultID, vaultDBFileName)
}

func copyPassword(password []byte) ([]byte, error) {
	if err := passwordpolicy.Validate(password); err != nil {
		return nil, ErrInvalidPassword
	}
	return append([]byte(nil), password...), nil
}

func generateID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		clear(raw)
		return "", fmt.Errorf("generate Desktop account identifier: %w", err)
	}
	defer clear(raw)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create Desktop account data root: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Desktop account data root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Desktop account data root must be a real directory")
	}
	if err := hardenPrivateDirectory(path); err != nil {
		return fmt.Errorf("secure Desktop account data root: %w", err)
	}
	return nil
}

func inspectLegacyPath(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy Desktop database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("legacy Desktop database path must be a regular file")
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return false, fmt.Errorf("open legacy Desktop database: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect opened legacy Desktop database: %w", err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return false, errors.New("legacy Desktop database changed while opening")
	}
	header := make([]byte, len(plaintextSQLiteHeader))
	defer clear(header)
	if _, err := io.ReadFull(file, header); err != nil {
		return false, fmt.Errorf("legacy Desktop database is truncated or unrecognized: %w", err)
	}
	if !bytes.Equal(header, []byte(plaintextSQLiteHeader)) {
		return false, errors.New("existing Desktop database is not recognized as the legacy plaintext SQLite format")
	}
	return true, nil
}

func rejectOrphanLegacyArtifacts(root string) error {
	for _, name := range []string{
		legacyDBFileName + ".bak",
		legacyDBFileName + "-wal",
		legacyDBFileName + "-shm",
		legacyDBFileName + "-journal",
		"snapshots",
		"vaults",
	} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("legacy Desktop artifact %q exists without its database; refusing automatic setup", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect legacy Desktop artifact %q: %w", name, err)
		}
	}
	return nil
}

func rejectPlaintextArtifactsWithManifest(root string) error {
	for _, name := range []string{
		legacyDBFileName,
		legacyDBFileName + ".bak",
		legacyDBFileName + "-wal",
		legacyDBFileName + "-shm",
		legacyDBFileName + "-journal",
		legacyDBFileName + ".bak-wal",
		legacyDBFileName + ".bak-shm",
		legacyDBFileName + ".bak-journal",
		"snapshots",
		legacyWorkDir,
		legacyQuarantineDir,
	} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("legacy Desktop artifact %q exists beside a configured encrypted account", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect legacy Desktop artifact %q: %w", name, err)
		}
	}
	return nil
}

func ensureFreshVaultPath(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("generated Desktop vault path already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect generated Desktop vault path: %w", err)
	}
	vaultDirectory := filepath.Dir(path)
	if err := ensurePrivateDirectory(filepath.Dir(vaultDirectory)); err != nil {
		return err
	}
	return ensurePrivateDirectory(vaultDirectory)
}

func validatePrivateVaultFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Desktop vault: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("Desktop vault must be a regular file, not a symbolic link")
	}
	if hasInsecurePermissions(info.Mode()) {
		return fmt.Errorf("Desktop vault must be owner-only: %04o", info.Mode().Perm())
	}
	return nil
}

func cleanupFreshVault(path string) {
	for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
		_ = os.Remove(path + suffix)
	}
	_ = os.Remove(filepath.Dir(path))
}
