package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/securedb"
)

func TestControlStoreUsesSQLCipher(t *testing.T) {
	var key securedb.RawKey
	for index := range key {
		key[index] = 0x74
	}
	opener := securedb.NewEncryptedOpener(key)
	defer opener.Destroy()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "control.db")
	store, err := Open(context.Background(), opener, path)
	if err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" &&
			(errors.Is(err, securedb.ErrCipherUnavailable) ||
				errors.Is(err, securedb.ErrCipherVersion) ||
				errors.Is(err, securedb.ErrCipherProvider)) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatalf("open encrypted control store: %v", err)
	}
	bootstrapTestAdmin(t, store)
	if err := opener.CheckIntegrity(context.Background(), store.db); err != nil {
		t.Fatalf("control database integrity: %v", err)
	}
	if err := securedb.RequireEncryptedHeader(path); err != nil {
		t.Fatalf("control database header: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if opener.Encrypted() {
		t.Fatal("Store.Close did not destroy its owned SQLCipher opener")
	}
}
