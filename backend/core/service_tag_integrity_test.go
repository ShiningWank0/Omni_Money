package core

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"omni_money/backend/database"
)

func TestCreateTagByPathTrimsAndIsAtomic(t *testing.T) {
	setupCoreTestDB(t)
	created, err := CreateTagByPath("  食費 / 外食 ")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "外食" {
		t.Fatalf("created leaf = %#v", created)
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM tags").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("tag count = %d, want 2", count)
	}
	if _, err := CreateTagByPath("new/child/"); err == nil {
		t.Fatal("empty path component was accepted")
	}
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM tags WHERE name='new'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed path left a partial tag")
	}
}

func TestCreateTagByPathPropagatesLookupErrors(t *testing.T) {
	setupCoreTestDB(t)
	if _, err := database.GetDB().Exec("DROP TABLE tags"); err != nil {
		t.Fatal(err)
	}
	_, err := CreateTagByPath("broken")
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("lookup error = %v, want non-no-rows propagation", err)
	}
}

func TestCreateTagByPathConcurrentCallsDoNotDuplicateRoots(t *testing.T) {
	setupCoreTestDB(t)
	const callers = 12
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := CreateTagByPath("同じ/子")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM tags WHERE parent_id IS NULL AND name='同じ'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("root count = %d", count)
	}
}

func TestTagNameValidationIsSharedByCreateAndUpdate(t *testing.T) {
	setupCoreTestDB(t)
	tag, err := CreateTag("  root  ", nil)
	if err != nil || tag.Name != "root" {
		t.Fatalf("create = %#v, err=%v", tag, err)
	}
	if err := UpdateTag(tag.ID, " hidden\u200bname "); err == nil {
		t.Fatal("unsafe rename was accepted")
	}
	if err := UpdateTag(999999, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing rename error = %v", err)
	}
	if _, err := CreateTag(strings.Repeat("a", 256), nil); err == nil {
		t.Fatal("overlong tag was accepted")
	}
}

func TestAddTransactionTagsIsAtomicOnUnknownTag(t *testing.T) {
	setupCoreTestDB(t)
	txID := insertTestTransaction(t, "cash", "2026-01-01", "item", "expense", 10, -10)
	first, err := CreateTag("first", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddTransactionTags(txID, []int64{first.ID, 999999}); err == nil {
		t.Fatal("unknown tag was accepted")
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_tags WHERE transaction_id = ?", txID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial tag links = %d", count)
	}
	if err := AddTransactionTags(txID, []int64{first.ID, first.ID}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_tags WHERE transaction_id = ?", txID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deduplicated tag links = %d", count)
	}
}
