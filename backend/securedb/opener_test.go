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
