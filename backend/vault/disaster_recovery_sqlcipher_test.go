package vault_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/vault"
)

// Exercise a cold copy of the real encrypted control DB and user vault,
// including rotated credential envelopes. Archive/retention mechanics are
// tested separately by scripts/backup-data-root_test.sh with a mock Docker CLI.
func TestSQLCipherColdRestoreRecoversVaultWithRotatedCredentials(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	controlKey := integrationKey(0x63)
	defer controlKey.Destroy()
	dek := integrationKey(0x75)
	defer dek.Destroy()
	password := []byte("original-backup-test-password")
	nextPassword := []byte("rotated-backup-test-password")
	recovery := bytes.Repeat([]byte{0x86}, keyenvelope.RecoverySecretSize)
	nextRecovery := bytes.Repeat([]byte{0x97}, keyenvelope.RecoverySecretSize)
	defer clear(password)
	defer clear(nextPassword)
	defer clear(recovery)
	defer clear(nextRecovery)
	const userID = "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
	const vaultID = "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
	binding := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	controlRel := filepath.Join("control", "omni_control.db")
	ledgerRel := filepath.Join("vaults", vaultID, "ledger.db")
	store, err := control.Open(ctx, securedb.NewEncryptedOpener(controlKey), filepath.Join(root, controlRel))
	if err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" && isUnavailableSQLCipher(err) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek[:], password, binding)
	if err != nil {
		t.Fatal(err)
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek[:], recovery, binding)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	if _, err := store.BootstrapFirstAdmin(ctx, control.BootstrapAdminInput{
		ID: userID, VaultID: vaultID, Email: "restore@example.com", DisplayName: "Restore Test",
		PasswordCredential: control.PasswordCredentialInput{Envelope: *passwordEnvelope},
		RecoveryEnvelope:   &control.RecoveryEnvelopeInput{Envelope: *recoveryEnvelope},
	}, now); err != nil {
		t.Fatal(err)
	}
	manager, err := vault.NewManager(filepath.Join(root, "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(ctx) })
	lease, err := manager.Acquire(userID, vaultID, dek)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	service, err := lease.Service()
	if err != nil {
		t.Fatal(err)
	}
	const marker = "issue-62-cold-restore-financial-canary"
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "cash", Date: "2026-10-18", Item: marker, Type: "income", Amount: 12345,
	}); err != nil {
		t.Fatal(err)
	}
	storedPassword, err := store.GetPasswordCredential(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	replacementPassword, err := keyenvelope.WrapWithPassword(dek[:], nextPassword, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePasswordCredential(ctx, userID, storedPassword,
		control.PasswordCredentialInput{Envelope: *replacementPassword}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	storedRecovery, err := store.GetActiveRecoveryEnvelope(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	replacementRecovery, err := keyenvelope.WrapWithRecovery(dek[:], nextRecovery, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateRecoveryEnvelope(ctx, userID, storedRecovery,
		control.RecoveryEnvelopeInput{Envelope: *replacementRecovery}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restoredRoot := t.TempDir()
	for _, relative := range []string{controlRel, ledgerRel} {
		content, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range [][]byte{[]byte(marker), controlKey[:], dek[:], password, nextPassword, recovery, nextRecovery} {
			if bytes.Contains(content, secret) {
				t.Fatalf("plaintext financial data or key material in backup file %s", relative)
			}
		}
		destination := filepath.Join(restoredRoot, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// No live files may be used to make the recovery check pass.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	wrongKey := integrationKey(0x64)
	defer wrongKey.Destroy()
	if wrongStore, err := control.Open(ctx, securedb.NewEncryptedOpener(wrongKey), filepath.Join(restoredRoot, controlRel)); err == nil {
		_ = wrongStore.Close()
		t.Fatal("restored control DB accepted a different control key")
	}
	restoredStore, err := control.Open(ctx, securedb.NewEncryptedOpener(controlKey), filepath.Join(restoredRoot, controlRel))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restoredVaultID, err := restoredStore.LookupVaultID(ctx, userID)
	if err != nil || restoredVaultID != vaultID {
		t.Fatalf("restored user binding: %q, %v", restoredVaultID, err)
	}
	credential, err := restoredStore.GetPasswordCredential(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	oldDEK, err := keyenvelope.UnwrapWithPassword(&credential.Envelope, password, binding)
	clear(oldDEK)
	if !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("retired password was not rejected by authentication: %v", err)
	}
	passwordDEK, err := keyenvelope.UnwrapWithPassword(&credential.Envelope, nextPassword, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(passwordDEK)
	recoveredEnvelope, err := restoredStore.GetActiveRecoveryEnvelope(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	oldDEK, err = keyenvelope.UnwrapWithRecovery(&recoveredEnvelope.Envelope, recovery, binding)
	clear(oldDEK)
	if !errors.Is(err, keyenvelope.ErrAuthentication) {
		t.Fatalf("retired recovery secret was not rejected by authentication: %v", err)
	}
	recoveredDEK, err := keyenvelope.UnwrapWithRecovery(&recoveredEnvelope.Envelope, nextRecovery, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(recoveredDEK)
	if !bytes.Equal(passwordDEK, dek[:]) || !bytes.Equal(recoveredDEK, dek[:]) {
		t.Fatal("credential rotation or cold restore changed the vault key")
	}
	restoredManager, err := vault.NewManager(filepath.Join(restoredRoot, "vaults"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredManager.Close(ctx)
	var restoredKey securedb.RawKey
	copy(restoredKey[:], recoveredDEK)
	defer restoredKey.Destroy()
	restoredLease, err := restoredManager.Acquire(userID, restoredVaultID, restoredKey)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredLease.Release()
	restoredService, err := restoredLease.Service()
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := restoredService.GetTransactions("cash", "")
	if err != nil || len(transactions) != 1 || transactions[0].Item != marker || transactions[0].Amount != 12345 || transactions[0].Balance != 12345 {
		t.Fatalf("recovered financial data differs: %+v, %v", transactions, err)
	}
}
