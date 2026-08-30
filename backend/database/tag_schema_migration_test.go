package database

import (
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
