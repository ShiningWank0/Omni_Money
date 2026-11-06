package securedb

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCheckpointTruncateAcceptsWALAndNonWALAndCancellation(t *testing.T) {
	ctx := context.Background()
	db, err := NewPlainOpener().Open(ctx, filepath.Join(t.TempDir(), "checkpoint.db"), Writable)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE sample(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sample VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	if err := CheckpointTruncate(ctx, db); err != nil {
		t.Fatalf("WAL checkpoint: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if err := CheckpointTruncate(ctx, db); err != nil {
		t.Fatalf("non-WAL checkpoint: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := CheckpointTruncate(cancelled, db); err != context.Canceled {
		t.Fatalf("cancelled checkpoint error = %v", err)
	}
}
