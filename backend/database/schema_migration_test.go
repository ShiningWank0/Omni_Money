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
