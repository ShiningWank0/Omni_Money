package desktopaccount

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omni_money/backend/securedb"
)

func TestSQLCipherCoordinatorCreatesAndReopensEncryptedVault(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := c.Setup(testPassword)
	if err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" && isUnavailableSQLCipher(err) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatalf("Setup encrypted Desktop account: %v", err)
	}
	defer clear(recovery)
	t.Cleanup(func() { _ = c.Close() })

	vaultPath := filepath.Join(c.root, "vaults", c.manifest.VaultID, vaultDBFileName)
	if err := securedb.RequireEncryptedHeader(vaultPath); err != nil {
		t.Fatalf("Desktop vault is not encrypted: %v", err)
	}
	if err := c.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := c.Unlock(testPassword); err != nil {
		t.Fatalf("reopen encrypted Desktop vault: %v", err)
	}
	if _, err := c.CreateSnapshot(); err != nil {
		t.Fatalf("create encrypted Desktop snapshot: %v", err)
	}
	snapshots, err := c.ListSnapshots()
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("encrypted snapshots = %#v, err=%v", snapshots, err)
	}
	snapshotPath := filepath.Join(filepath.Dir(vaultPath), "snapshots", snapshots[0])
	if err := securedb.RequireEncryptedHeader(snapshotPath); err != nil {
		t.Fatalf("Desktop snapshot is not encrypted: %v", err)
	}
}

func isUnavailableSQLCipher(err error) bool {
	return errors.Is(err, securedb.ErrCipherUnavailable) ||
		errors.Is(err, securedb.ErrCipherVersion) ||
		errors.Is(err, securedb.ErrCipherProvider) ||
		errors.Is(err, securedb.ErrCipherMemorySecurity)
}
