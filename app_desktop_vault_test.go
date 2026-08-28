package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"

	"omni_money/backend/desktopaccount"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/models"
)

func TestNewAppStartsLockedWithoutCreatingDatabase(t *testing.T) {
	root := t.TempDir()
	app, err := NewApp(root)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { app.shutdown(context.Background()) })

	status := app.GetDesktopVaultStatus()
	if status.Configured || status.Unlocked || status.LegacyMigrationRequired || status.Role != "" {
		t.Fatalf("unexpected initial status: %+v", status)
	}

	var entries []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			entries = append(entries, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk data root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("constructing a locked App wrote vault data: %v", entries)
	}
}

func TestLockedFinancialBindingsFailClosed(t *testing.T) {
	app := newAppWithCoordinator(&fakeDesktopCoordinator{
		status: desktopaccount.Status{Configured: true, Unlocked: false, Role: desktopaccount.RoleAdmin},
	})

	tests := []struct {
		name string
		call func() error
	}{
		{"GetAccounts", func() error { _, err := app.GetAccounts(); return err }},
		{"GetTransactions", func() error { _, err := app.GetTransactions("", ""); return err }},
		{"AddTransaction", func() error { _, err := app.AddTransaction(models.TransactionRequest{}); return err }},
		{"UpdateTransaction", func() error { _, err := app.UpdateTransaction(1, models.TransactionRequest{}); return err }},
		{"DeleteTransaction", func() error { return app.DeleteTransaction(1) }},
		{"GetBalanceHistory", func() error { _, err := app.GetBalanceHistory(); return err }},
		{"GetBalanceHistoryFiltered", func() error { _, err := app.GetBalanceHistoryFiltered(nil); return err }},
		{"GetItems", func() error { _, err := app.GetItems(""); return err }},
		{"GetCreditCardSettings", func() error { _, err := app.GetCreditCardSettings(); return err }},
		{"SaveCreditCardSettings", func() error { return app.SaveCreditCardSettings(nil) }},
		{"GetBankAccountSettings", func() error { _, err := app.GetBankAccountSettings(); return err }},
		{"SaveBankAccountSettings", func() error { return app.SaveBankAccountSettings(nil) }},
		{"BackupToCSVFile", func() error { _, err := app.BackupToCSVFile(); return err }},
		{"ImportCSV", func() error { _, err := app.ImportCSV("", ""); return err }},
		{"CreateSnapshot", func() error { _, err := app.CreateSnapshot(); return err }},
		{"ListSnapshots", func() error { _, err := app.ListSnapshots(); return err }},
		{"RestoreSnapshot", func() error { return app.RestoreSnapshot("snapshot.db") }},
		{"AddTransactionImage", func() error { _, err := app.AddTransactionImage(1, models.TransactionImageRequest{}); return err }},
		{"GetTransactionImages", func() error { _, err := app.GetTransactionImages(1); return err }},
		{"DeleteTransactionImage", func() error { return app.DeleteTransactionImage(1) }},
		{"CreateTag", func() error { _, err := app.CreateTag("tag", nil); return err }},
		{"CreateTagByPath", func() error { _, err := app.CreateTagByPath("tag"); return err }},
		{"GetTags", func() error { _, err := app.GetTags(); return err }},
		{"UpdateTag", func() error { return app.UpdateTag(1, "tag") }},
		{"DeleteTag", func() error { return app.DeleteTag(1) }},
		{"GetTransactionTags", func() error { _, err := app.GetTransactionTags(1); return err }},
		{"AddTransactionTags", func() error { return app.AddTransactionTags(1, nil) }},
		{"RemoveTransactionTag", func() error { return app.RemoveTransactionTag(1, 1) }},
		{"GetTagSummary", func() error { _, err := app.GetTagSummary("", "", ""); return err }},
		{"GetTransactionLinks", func() error { _, err := app.GetTransactionLinks(1); return err }},
		{"AddTransactionLink", func() error { return app.AddTransactionLink(1, 2) }},
		{"RemoveTransactionLink", func() error { return app.RemoveTransactionLink(1, 2) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, desktopaccount.ErrLocked) {
				t.Fatalf("got %v, want desktopaccount.ErrLocked", err)
			}
		})
	}
}

func TestBackupToCSVFileRejectsLockBoundaryAcrossDialog(t *testing.T) {
	coordinator := &fakeDesktopCoordinator{
		status: desktopaccount.Status{Configured: true, Unlocked: true, Role: desktopaccount.RoleAdmin},
	}
	app := newAppWithCoordinator(coordinator)
	app.startup(context.Background())
	app.chooseCSVDirectory = func(context.Context) (string, error) {
		if _, err := app.LockDesktopVault(); err != nil {
			t.Fatalf("LockDesktopVault: %v", err)
		}
		return t.TempDir(), nil
	}

	if _, err := app.BackupToCSVFile(); !errors.Is(err, ErrDesktopVaultChanged) {
		t.Fatalf("got %v, want ErrDesktopVaultChanged", err)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.serviceCalls != 0 {
		t.Fatalf("Service called %d times after lock boundary, want 0", coordinator.serviceCalls)
	}
}

func TestRecoverDesktopVaultRequiresStrictRawURLRecoveryCode(t *testing.T) {
	coordinator := &fakeDesktopCoordinator{
		status:             desktopaccount.Status{Configured: true},
		nextRecoverySecret: bytesOf(0x5a, keyenvelope.RecoverySecretSize),
	}
	app := newAppWithCoordinator(coordinator)
	secret := bytesOf(0x2c, keyenvelope.RecoverySecretSize)

	padded := base64.URLEncoding.EncodeToString(secret)
	if _, err := app.RecoverDesktopVault(padded, "a-valid-password"); !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("padded code got %v, want authentication error", err)
	}
	short := base64.RawURLEncoding.EncodeToString(secret[:len(secret)-1])
	if _, err := app.RecoverDesktopVault(short, "a-valid-password"); !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("short code got %v, want authentication error", err)
	}

	raw := base64.RawURLEncoding.EncodeToString(secret)
	response, err := app.RecoverDesktopVault(raw, "a-valid-password")
	if err != nil {
		t.Fatalf("RecoverDesktopVault: %v", err)
	}
	if !response.Status.Unlocked {
		t.Fatalf("recovery response stayed locked: %+v", response.Status)
	}
	wantNext := base64.RawURLEncoding.EncodeToString(bytesOf(0x5a, keyenvelope.RecoverySecretSize))
	if response.RecoveryCode != wantNext {
		t.Fatalf("recovery code = %q, want %q", response.RecoveryCode, wantNext)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if string(coordinator.recoveredWith) != string(secret) {
		t.Fatal("coordinator did not receive the decoded recovery secret")
	}
}

func TestMigrateLegacyDesktopVaultClearsPasswordAndRequiresDeliveryAcknowledgement(t *testing.T) {
	coordinator := &fakeDesktopCoordinator{
		status:             desktopaccount.Status{LegacyMigrationRequired: true},
		nextRecoverySecret: bytesOf(0x6b, keyenvelope.RecoverySecretSize),
	}
	app := newAppWithCoordinator(coordinator)

	response, err := app.MigrateLegacyDesktopVault("migration-password")
	if err != nil {
		t.Fatalf("MigrateLegacyDesktopVault: %v", err)
	}
	if !response.Status.Unlocked || !response.Status.Configured {
		t.Fatalf("migration response did not report the opened vault: %+v", response.Status)
	}
	wantCode := base64.RawURLEncoding.EncodeToString(bytesOf(0x6b, keyenvelope.RecoverySecretSize))
	if response.RecoveryCode != wantCode {
		t.Fatalf("recovery code = %q, want %q", response.RecoveryCode, wantCode)
	}
	if app.generation != 1 {
		t.Fatalf("generation after migration = %d, want 1", app.generation)
	}
	for i, value := range coordinator.migratedPassword {
		if value != 0 {
			t.Fatalf("migration password copy byte %d was not cleared", i)
		}
	}
	if coordinator.acknowledgeCalls != 0 {
		t.Fatal("migration implicitly acknowledged recovery delivery")
	}

	status, err := app.AcknowledgeDesktopVaultRecovery()
	if err != nil {
		t.Fatalf("AcknowledgeDesktopVaultRecovery: %v", err)
	}
	if !status.Unlocked || coordinator.acknowledgeCalls != 1 {
		t.Fatalf("acknowledgement status/calls = %+v/%d", status, coordinator.acknowledgeCalls)
	}
	if app.generation != 2 {
		t.Fatalf("generation after acknowledgement = %d, want 2", app.generation)
	}
}

func TestAcknowledgeDesktopVaultRecoveryIsUsableWithoutMigrationJournal(t *testing.T) {
	coordinator := &fakeDesktopCoordinator{
		status: desktopaccount.Status{Configured: true, Unlocked: true, Role: desktopaccount.RoleAdmin},
	}
	app := newAppWithCoordinator(coordinator)

	status, err := app.AcknowledgeDesktopVaultRecovery()
	if err != nil {
		t.Fatalf("AcknowledgeDesktopVaultRecovery: %v", err)
	}
	if !status.Unlocked || coordinator.acknowledgeCalls != 1 {
		t.Fatalf("idempotent acknowledgement status/calls = %+v/%d", status, coordinator.acknowledgeCalls)
	}
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for i := range result {
		result[i] = value
	}
	return result
}

type fakeDesktopCoordinator struct {
	mu                 sync.Mutex
	status             desktopaccount.Status
	serviceCalls       int
	recoveredWith      []byte
	migratedPassword   []byte
	acknowledgeCalls   int
	nextRecoverySecret []byte
}

func (c *fakeDesktopCoordinator) Status() desktopaccount.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *fakeDesktopCoordinator) Setup([]byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = desktopaccount.Status{Configured: true, Unlocked: true, Role: desktopaccount.RoleAdmin}
	return bytesOf(1, keyenvelope.RecoverySecretSize), nil
}

func (c *fakeDesktopCoordinator) MigrateLegacy(password []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Retain the exact temporary argument so the test can verify that the App
	// clears its byte copy after the coordinator call returns.
	c.migratedPassword = password
	c.status = desktopaccount.Status{Configured: true, Unlocked: true, Role: desktopaccount.RoleAdmin}
	return append([]byte(nil), c.nextRecoverySecret...), nil
}

func (c *fakeDesktopCoordinator) AcknowledgeRecovery() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acknowledgeCalls++
	return nil
}

func (c *fakeDesktopCoordinator) Unlock([]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Unlocked = true
	return nil
}

func (c *fakeDesktopCoordinator) Recover(secret, _ []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recoveredWith = append([]byte(nil), secret...)
	c.status.Unlocked = true
	return append([]byte(nil), c.nextRecoverySecret...), nil
}

func (c *fakeDesktopCoordinator) ChangePassword([]byte, []byte) error { return nil }

func (c *fakeDesktopCoordinator) RotateRecovery([]byte) ([]byte, error) {
	return bytesOf(2, keyenvelope.RecoverySecretSize), nil
}

func (c *fakeDesktopCoordinator) Service() (*desktopaccount.ServiceLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serviceCalls++
	return nil, desktopaccount.ErrLocked
}

func (c *fakeDesktopCoordinator) CreateSnapshot() (string, error) {
	return "", desktopaccount.ErrLocked
}

func (c *fakeDesktopCoordinator) ListSnapshots() ([]string, error) {
	return nil, desktopaccount.ErrLocked
}

func (c *fakeDesktopCoordinator) RestoreSnapshot(string) error { return desktopaccount.ErrLocked }

func (c *fakeDesktopCoordinator) Lock() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Unlocked = false
	return nil
}

func (c *fakeDesktopCoordinator) Close() error { return nil }
