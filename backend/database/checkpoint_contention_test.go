package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
	"omni_money/backend/securedb"
)

func TestRestoreSnapshotKeepsLiveDatabaseWhenCheckpointIsBusy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	instance, err := OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	db := instance.DB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("INSERT INTO settings(key,value) VALUES('marker','snapshot')"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := instance.CreateSnapshot(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := securedb.NewPlainOpener().Open(context.Background(), path, securedb.Writable)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var marker string
	if err := tx.QueryRow("SELECT value FROM settings WHERE key='marker'").Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE settings SET value='live' WHERE key='marker'"); err != nil {
		t.Fatal(err)
	}
	// Shorten only this test connection's existing five-second wait.
	if _, err := db.Exec("PRAGMA busy_timeout=20"); err != nil {
		t.Fatal(err)
	}
	err = instance.RestoreSnapshot(filepath.Join(dir, "snapshots"), filepath.Base(snapshot))
	var busy sqlite3.Error
	if !errors.As(err, &busy) || busy.Code != sqlite3.ErrBusy {
		t.Fatalf("restore error = %v, want checkpoint busy", err)
	}
	if instance.DB() != db {
		t.Fatal("busy checkpoint replaced the live database handle")
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='marker'").Scan(&marker); err != nil || marker != "live" {
		t.Fatalf("failed restore changed live data: marker=%q, err=%v", marker, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(filepath.Join(dir, "snapshots"), filepath.Base(snapshot)); err != nil {
		t.Fatalf("restore failed after reader released checkpoint: %v", err)
	}
	if err := instance.DB().QueryRow("SELECT value FROM settings WHERE key='marker'").Scan(&marker); err != nil || marker != "snapshot" {
		t.Fatalf("restore after reader release: marker=%q, err=%v", marker, err)
	}
}
