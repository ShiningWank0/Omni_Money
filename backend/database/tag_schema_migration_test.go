package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestTagRootPartialUniqueIndexRejectsDuplicateAndMigrationIsFailClosed(t *testing.T) {
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
	if err := createTablesOn(instance.DB()); err == nil || !strings.Contains(err.Error(), "rootタグ名が重複") {
		t.Fatalf("duplicate migration error = %v", err)
	}
	var version int
	if err := instance.DB().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("failed migration changed version to %d", version)
	}
	var indexCount int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_tags_root_name_unique'").Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 0 {
		t.Fatal("failed migration left the partial index behind")
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
