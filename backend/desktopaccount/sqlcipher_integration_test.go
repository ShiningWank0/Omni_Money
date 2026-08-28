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

func TestSQLCipherCoordinatorMigratesLegacyVaultAndSnapshots(t *testing.T) {
	root := t.TempDir()
	createLegacyTestDatabase(t, filepath.Join(root, legacyDBFileName))
	if err := os.Mkdir(filepath.Join(root, "snapshots"), 0700); err != nil {
		t.Fatal(err)
	}
	createLegacyTestDatabase(t, filepath.Join(root, "snapshots", "legacy-snapshot.db"))

	c, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := c.MigrateLegacy(testPassword)
	if err != nil {
		if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" && isUnavailableSQLCipher(err) {
			t.Skipf("SQLCipher integration build is not active: %v", err)
		}
		t.Fatalf("migrate encrypted Desktop account: %v", err)
	}
	defer clear(recovery)
	t.Cleanup(func() { _ = c.Close() })
	if c.Status().Unlocked || !c.Status().LegacyMigrationRequired {
		t.Fatalf("migration delivery status = %+v", c.Status())
	}
	vaultPath := filepath.Join(root, "vaults", c.manifest.VaultID, vaultDBFileName)
	if err := securedb.RequireEncryptedHeader(vaultPath); err != nil {
		t.Fatalf("migrated Desktop vault is not encrypted: %v", err)
	}
	snapshotPath := filepath.Join(root, "vaults", c.manifest.VaultID, "snapshots", "legacy-snapshot.db")
	if err := securedb.RequireEncryptedHeader(snapshotPath); err != nil {
		t.Fatalf("migrated Desktop snapshot is not encrypted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, legacyDBFileName)); !os.IsNotExist(err) {
		t.Fatalf("legacy plaintext vault remains: %v", err)
	}
	if err := c.AcknowledgeRecovery(); err != nil {
		t.Fatal(err)
	}
	if err := c.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := c.Unlock(testPassword); err != nil {
		t.Fatalf("unlock migrated Desktop vault: %v", err)
	}
}

func isUnavailableSQLCipher(err error) bool {
	return errors.Is(err, securedb.ErrCipherUnavailable) ||
		errors.Is(err, securedb.ErrCipherVersion) ||
		errors.Is(err, securedb.ErrCipherProvider) ||
		errors.Is(err, securedb.ErrCipherMemorySecurity)
}
