package vault_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/core"
	"omni_money/backend/models"
	"omni_money/backend/securedb"
	"omni_money/backend/vault"
)

func TestSQLCipherVaultManagerKeepsUsersEncryptedAndIsolated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vaults")
	manager, err := vault.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	keyA := integrationKey(0x31)
	keyB := integrationKey(0x42)
	leaseA, err := manager.Acquire("user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", keyA)
	if err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" && isUnavailableSQLCipher(err) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatalf("open first SQLCipher vault: %v", err)
	}
	leaseB, err := manager.Acquire("user_01HZX7CYK3XPSJ0HE8P2RQ7V4N", "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", keyB)
	if err != nil {
		leaseA.Release()
		t.Fatalf("open second SQLCipher vault: %v", err)
	}

	serviceA, err := leaseA.Service()
	if err != nil {
		t.Fatal(err)
	}
	serviceB, err := leaseB.Service()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		service *core.Service
		account string
	}{
		{service: serviceA, account: "only-user-a"},
		{service: serviceB, account: "only-user-b"},
	} {
		if _, err := test.service.AddTransaction(models.TransactionRequest{
			Account: test.account,
			Date:    "2026-08-28",
			Item:    "encrypted-sentinel",
			Type:    "expense",
			Amount:  100,
		}); err != nil {
			t.Fatal(err)
		}
	}
	accountsA, err := serviceA.GetAccounts()
	if err != nil || len(accountsA) != 1 || accountsA[0] != "only-user-a" {
		t.Fatalf("user A accounts = %v, %v", accountsA, err)
	}
	accountsB, err := serviceB.GetAccounts()
	if err != nil || len(accountsB) != 1 || accountsB[0] != "only-user-b" {
		t.Fatalf("user B accounts = %v, %v", accountsB, err)
	}

	leaseA.Release()
	leaseB.Release()
	if err := manager.CloseUser(context.Background(), "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M"); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseUser(context.Background(), "user_01HZX7CYK3XPSJ0HE8P2RQ7V4N"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", "ledger.db"),
		filepath.Join(root, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", "ledger.db"),
	} {
		if err := securedb.RequireEncryptedHeader(path); err != nil {
			t.Fatalf("vault %s has a plaintext header: %v", path, err)
		}
	}
}

func integrationKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}

func isUnavailableSQLCipher(err error) bool {
	return errors.Is(err, securedb.ErrCipherUnavailable) ||
		errors.Is(err, securedb.ErrCipherVersion) ||
		errors.Is(err, securedb.ErrCipherProvider)
}
