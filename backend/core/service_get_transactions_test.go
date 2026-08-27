package core

import (
	"reflect"
	"testing"

	"omni_money/backend/database"
)

func TestGetTransactionsAssignsTagsInExistingOrder(t *testing.T) {
	setupCoreTestDB(t)

	firstID := insertTestTransaction(t, "cash", "2026-01-01", "first", "expense", 100, -100)
	secondID := insertTestTransaction(t, "cash", "2026-01-02", "second", "expense", 200, -300)
	thirdID := insertTestTransaction(t, "cash", "2026-01-03", "third", "expense", 300, -600)

	alphaID := insertGetTransactionsTestTag(t, "Alpha", nil, 1)
	zuluID := insertGetTransactionsTestTag(t, "Zulu", nil, 1)
	betaID := insertGetTransactionsTestTag(t, "Beta", &zuluID, 2)

	// Insert in a different order to ensure the response keeps level, name ordering.
	linkGetTransactionsTestTag(t, firstID, betaID)
	linkGetTransactionsTestTag(t, firstID, zuluID)
	linkGetTransactionsTestTag(t, firstID, alphaID)
	linkGetTransactionsTestTag(t, secondID, betaID)

	transactions, err := GetTransactions("cash", "")
	if err != nil {
		t.Fatalf("GetTransactions failed: %v", err)
	}
	if len(transactions) != 3 {
		t.Fatalf("transaction count = %d, want 3", len(transactions))
	}

	wantTags := map[int64][]string{
		firstID:  {"Alpha", "Zulu", "Beta"},
		secondID: {"Beta"},
		thirdID:  {},
	}
	for _, transaction := range transactions {
		got := make([]string, 0, len(transaction.Tags))
		for _, tag := range transaction.Tags {
			got = append(got, tag.Name)
		}
		if !reflect.DeepEqual(got, wantTags[transaction.ID]) {
			t.Errorf("transaction %d tags = %#v, want %#v", transaction.ID, got, wantTags[transaction.ID])
		}
	}
}

func insertGetTransactionsTestTag(t *testing.T, name string, parentID *int64, level int) int64 {
	t.Helper()
	result, err := database.GetDB().Exec(
		"INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)",
		name, parentID, level,
	)
	if err != nil {
		t.Fatalf("insert tag failed: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tag LastInsertId failed: %v", err)
	}
	return id
}

func linkGetTransactionsTestTag(t *testing.T, transactionID, tagID int64) {
	t.Helper()
	if _, err := database.GetDB().Exec(
		"INSERT INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)",
		transactionID, tagID,
	); err != nil {
		t.Fatalf("link transaction tag failed: %v", err)
	}
}
