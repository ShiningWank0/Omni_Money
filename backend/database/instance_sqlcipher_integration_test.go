package database

import (
	"errors"
	"path/filepath"
	"testing"

	"omni_money/backend/securedb"
)

func TestEncryptedInstancesKeepKeysAndSnapshotsIsolated(t *testing.T) {
	root := t.TempDir()
	firstKey := filledRawKey(0x51)
	secondKey := filledRawKey(0x62)
	firstPath := filepath.Join(root, "first", "vault.db")
	secondPath := filepath.Join(root, "second", "vault.db")

	first, err := OpenEncryptedInstance(firstPath, firstKey)
	if errors.Is(err, securedb.ErrCipherUnavailable) {
		t.Skip("test requires the pinned SQLCipher runtime")
	}
	if err != nil {
		t.Fatalf("open first encrypted instance: %v", err)
	}
	second, err := OpenEncryptedInstance(secondPath, secondKey)
	if err != nil {
		_ = first.Close()
		t.Fatalf("open second encrypted instance: %v", err)
	}
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	insertTransactionItem(t, first, "first-encrypted")
	insertTransactionItem(t, second, "second-encrypted")
	assertTransactionItem(t, first, "first-encrypted")
	assertTransactionItem(t, second, "second-encrypted")

	firstSnapshot, err := first.CreateSnapshot("")
	if err != nil {
		t.Fatalf("create encrypted snapshot: %v", err)
	}
	if err := securedb.RequireEncryptedHeader(firstPath); err != nil {
		t.Fatalf("first database is not encrypted: %v", err)
	}
	if err := securedb.RequireEncryptedHeader(secondPath); err != nil {
		t.Fatalf("second database is not encrypted: %v", err)
	}
	if err := securedb.RequireEncryptedHeader(firstSnapshot); err != nil {
		t.Fatalf("first snapshot is not encrypted: %v", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first encrypted instance: %v", err)
	}
	wrongKeyInstance, err := OpenEncryptedInstance(firstPath, secondKey)
	if err == nil {
		_ = wrongKeyInstance.Close()
		t.Fatal("opened first encrypted instance with second instance key")
	}
	reopened, err := OpenEncryptedInstance(firstPath, firstKey)
	if err != nil {
		t.Fatalf("reopen first encrypted instance: %v", err)
	}
	first = reopened
	assertTransactionItem(t, reopened, "first-encrypted")
	assertTransactionItem(t, second, "second-encrypted")
}

func filledRawKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}
