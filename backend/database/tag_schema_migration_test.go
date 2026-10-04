package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestTagRootPartialUniqueIndexArchivesDuplicateRootsLosslessly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate-root.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("DROP INDEX idx_tags_root_name_unique"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DB().Exec(`INSERT INTO tags(name, parent_id, level) VALUES ('same', NULL, 1), ('same', NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DB().Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(instance.DB()); err != nil {
		t.Fatalf("duplicate migration error = %v", err)
	}
	var version int
	if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != ledgerSchemaVersion {
		t.Fatalf("migration version = %d", version)
	}
	var indexCount int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_tags_root_name_unique'").Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatal("migration did not recreate the partial index")
	}
	var roots, archived int
	if err := instance.DB().QueryRow("SELECT COUNT(*), COALESCE(SUM(legacy_duplicate), 0) FROM tags WHERE name = 'same' AND parent_id IS NULL").Scan(&roots, &archived); err != nil {
		t.Fatal(err)
	}
	if roots != 2 || archived != 1 {
		t.Fatalf("duplicate roots were not preserved: count=%d archived=%d", roots, archived)
	}
}

func TestTagRootPartialUniqueIndexExistsOnNewLedger(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	var unique int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_tags_root_name_unique'").Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if unique != 1 {
		t.Fatalf("root partial index count = %d", unique)
	}
	if _, err := instance.DB().Exec("INSERT INTO tags(name, parent_id, level) VALUES ('same', NULL, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DB().Exec("INSERT INTO tags(name, parent_id, level) VALUES ('same', NULL, 1)"); err == nil {
		t.Fatal("duplicate root tag was accepted")
	} else if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unexpected no rows error")
	}
}

func TestVersionFiveLedgerMigratesSaveQueueWithoutWeakeningValidation(t *testing.T) {
	for _, forged := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "forged"}[forged], func(t *testing.T) {
			instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "v5.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			for _, statement := range []string{"DROP TABLE transaction_save_requests", "PRAGMA user_version = 5", "INSERT INTO transactions(account, date, item, type, amount, balance, memo) VALUES('cash', '2026-10-04 00:00:00', 'existing', 'income', 100, 100, '')"} {
				if _, err := instance.DB().Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if forged {
				if _, err := instance.DB().Exec("DROP INDEX idx_tags_root_name_unique; CREATE INDEX idx_tags_root_name_unique ON tags(name)"); err != nil {
					t.Fatal(err)
				}
			}
			err = createTablesOn(instance.DB())
			if forged {
				if err == nil {
					t.Fatal("v5 forged schema passed the migration trust boundary")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var version, count int
			if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != ledgerSchemaVersion {
				t.Fatalf("version=%d error=%v", version, err)
			}
			if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions WHERE item='existing'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("existing rows=%d error=%v", count, err)
			}
			if err := validateTransactionSaveSchema(context.Background(), instance.DB()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTagMigrationRepairsSameNamedNonUniqueIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong-index.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if _, err := instance.DB().Exec("DROP INDEX idx_tags_root_name_unique; CREATE INDEX idx_tags_root_name_unique ON tags(name)"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DB().Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(instance.DB()); err != nil {
		t.Fatalf("wrong index migration error = %v", err)
	}
	var version int
	if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != ledgerSchemaVersion {
		t.Fatalf("wrong index migration version = %d", version)
	}
}
