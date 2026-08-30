package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestLedgerSchemaRecordsCurrentVersion(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	var version int
	if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != ledgerSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, ledgerSchemaVersion)
	}
}

func TestLedgerSchemaV4AddsArchiveSidecarsWithoutChangingCurrentRows(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Build the current shape once, then remove only the additive v4 objects to
	// model a v3 ledger. Existing constrained transaction/image rows must survive
	// the atomic additive migration unchanged.
	if err := createTablesOn(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'kept', 'income', 42, 42, 'memo')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER trg_transaction_image_archive_quota_insert`,
		`DROP TRIGGER trg_transaction_images_quota_insert`,
		`DROP TABLE transaction_image_archive`,
		`DROP TABLE transaction_archive_amounts`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare v3 schema %q: %v", statement, err)
		}
	}
	if err := createTablesOn(db); err != nil {
		t.Fatalf("v3 to v4 migration: %v", err)
	}
	var amount, balance int64
	if err := db.QueryRow(`SELECT amount, balance FROM transactions WHERE item = 'kept'`).Scan(&amount, &balance); err != nil {
		t.Fatal(err)
	}
	if amount != 42 || balance != 42 {
		t.Fatalf("current row changed during migration: %d/%d", amount, balance)
	}
	for _, table := range []string{"transaction_archive_amounts", "transaction_image_archive"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("archive table %s = %d/%v", table, count, err)
		}
	}
}
func TestLedgerSchemaMigrationFailureRollsBackAtomically(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE transactions (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	if err := createTablesOn(db); err == nil {
		t.Fatal("incompatible legacy schema was accepted")
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed migration changed schema version to %d", version)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'transaction_links'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left a partially-created transaction_links table")
	}
}
