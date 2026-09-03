package securedb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDestroyedOpenerNeverFallsBackToPlaintext(t *testing.T) {
	for _, test := range []struct {
		name   string
		opener *Opener
	}{
		{name: "encrypted", opener: NewEncryptedOpener(RawKey{0: 1})},
		{name: "plain", opener: NewPlainOpener()},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.opener.Destroy()
			path := filepath.Join(t.TempDir(), "must-not-open.db")
			db, err := test.opener.Open(context.Background(), path, Writable)
			if db != nil {
				_ = db.Close()
				t.Fatal("destroyed opener returned a database handle")
			}
			if !errors.Is(err, ErrDestroyed) {
				t.Fatalf("Open error = %v, want ErrDestroyed", err)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destroyed opener created a database file: %v", statErr)
			}
		})
	}
}

func TestPlainReadOnlyPurposeCannotCreateOrWrite(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "missing.db")
	opener := NewPlainOpener()
	missing, err := opener.Open(context.Background(), missingPath, ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.PingContext(context.Background()); err == nil {
		_ = missing.Close()
		t.Fatal("read-only opener created a missing database")
	}
	_ = missing.Close()
	if _, err := os.Stat(missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only opener created a file: %v", err)
	}

	path := filepath.Join(dir, "existing.db")
	writable, err := opener.Open(context.Background(), path, Writable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec("CREATE TABLE sample (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := opener.Open(context.Background(), path, ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.Exec("INSERT INTO sample DEFAULT VALUES"); err == nil {
		t.Fatal("read-only opener accepted a write")
	}
}

func TestPlainImmutableReadOnlyPurposeCannotCreateOrWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "immutable.db")
	opener := NewPlainOpener()
	writable, err := opener.Open(context.Background(), path, Writable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec("CREATE TABLE sample (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := opener.Open(context.Background(), path, ImmutableReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if err := readOnly.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Exec("INSERT INTO sample DEFAULT VALUES"); err == nil {
		t.Fatal("immutable read-only opener accepted a write")
	}
}
