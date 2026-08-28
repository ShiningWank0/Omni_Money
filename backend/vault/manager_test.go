package vault

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/securedb"
)

const testVaultID = "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M"

func testKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}

func newPlainTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := newManager(t.TempDir(), func(path string, _ securedb.RawKey) (*database.Instance, error) {
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAcquireReusesOnlyExactBinding(t *testing.T) {
	manager := newPlainTestManager(t)
	first, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	if first.Instance() != second.Instance() || first.DB() == nil {
		t.Fatal("exact binding did not reuse the open instance")
	}
	if _, err := manager.Acquire("user-1", "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", testKey(0x11)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("vault mismatch error = %v", err)
	}
	if _, err := manager.Acquire("user-1", testVaultID, testKey(0x12)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("key mismatch error = %v", err)
	}
	first.Release()
	second.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsVaultSharedAcrossUsers(t *testing.T) {
	manager := newPlainTestManager(t)
	first, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		first.Release()
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x11)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("shared vault with same key error = %v", err)
	}
	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x12)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("shared vault with different key error = %v", err)
	}
}

func TestCloseUserWaitsForLeaseAndRejectsNewLease(t *testing.T) {
	manager := newPlainTestManager(t)
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x21))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()

	deadline := time.Now().Add(time.Second)
	for {
		var probe *Lease
		probe, err = manager.Acquire("user-1", testVaultID, testKey(0x21))
		if errors.Is(err, ErrDraining) {
			break
		}
		if err == nil {
			probe.Release()
			time.Sleep(time.Millisecond)
			continue
		}
		if time.Now().After(deadline) {
			t.Fatalf("vault did not enter draining state: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatalf("CloseUser returned before release: %v", err)
	default:
	}
	lease.Release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if lease.DB() != nil || lease.Instance() != nil {
		t.Fatal("released lease retained database access")
	}
}

func TestCloseTimeoutFailsClosed(t *testing.T) {
	manager := newPlainTestManager(t)
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x31))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x31)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Acquire after timed-out Close error = %v", err)
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVaultIdentifierCannotEscapeRoot(t *testing.T) {
	manager := newPlainTestManager(t)
	for _, value := range []string{"../outside-vault-id", "vault/with/slash-123", "short"} {
		if _, err := manager.Acquire("user-1", value, testKey(0x41)); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("Acquire(%q) error = %v", value, err)
		}
	}
	want := filepath.Join(manager.root, testVaultID, "ledger.db")
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x41))
	if err != nil {
		t.Fatal(err)
	}
	if lease.Instance() == nil {
		t.Fatal("valid vault did not open")
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(want)) != manager.root {
		t.Fatal("vault path escaped manager root")
	}
}
