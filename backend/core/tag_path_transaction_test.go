package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/database"
)

// A concurrent writer must be allowed to commit before the tag path reads
// its snapshot. Retrying an already-stale deferred transaction is too late.
func TestTagPathWriterWaitsBeforeReadingAndKeepsOtherVaultsIndependent(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()
	other, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		blocker.Close()
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, "INSERT INTO tags (name, parent_id, level) VALUES ('held-root', NULL, 1)"); err != nil {
		blocker.ExecContext(context.Background(), "ROLLBACK")
		blocker.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := createTagPathIn(db, []string{"held-root", "child"}); done <- err }()
	received := false
	defer func() {
		blocker.ExecContext(context.Background(), "ROLLBACK")
		blocker.Close()
		if !received {
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Error("tag writer did not drain after releasing its blocker")
			}
		}
	}()
	independent := make(chan error, 1)
	go func() { _, err := createTagPathIn(other.DB(), []string{"independent"}); independent <- err }()
	select {
	case err := <-independent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one vault's writer blocked an independent vault")
	}
	select {
	case err := <-done:
		received = true
		t.Fatalf("tag creation completed before the competing writer released its lock: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := blocker.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		received = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("tag creation did not finish after writer commit")
	}
	var roots, children int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags WHERE name='held-root' AND parent_id IS NULL").Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags c JOIN tags p ON c.parent_id=p.id WHERE p.name='held-root' AND c.name='child'").Scan(&children); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || children != 1 {
		t.Fatalf("roots=%d children=%d, want one of each", roots, children)
	}
}

func TestTagPathWriterRollsBackPartialPathAndReturnsConnection(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TRIGGER reject_tag_path BEFORE INSERT ON tags WHEN NEW.name='rejected' BEGIN SELECT RAISE(ABORT, 'injected tag failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := createTagPathIn(db, []string{"partial-root", "rejected"}); err == nil || !strings.Contains(err.Error(), "injected tag failure") {
		t.Fatalf("tag creation error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags WHERE name='partial-root'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed path left a partial root")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_tag_path"); err != nil {
		t.Fatal(err)
	}
	if _, err := createTagPathIn(db, []string{"next-root", "child"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
