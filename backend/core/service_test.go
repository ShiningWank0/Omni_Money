package core

import (
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
)

func setupCoreTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "omni_money_test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(database.CloseDB)
}

func insertTestTransaction(t *testing.T, account, date, item, txType string, amount, balance int64) int64 {
	t.Helper()
	result, err := database.GetDB().Exec(
		"INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, ?, ?, ?, ?, ?, '')",
		account, date, item, txType, amount, balance,
	)
	if err != nil {
		t.Fatalf("insert transaction failed: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId failed: %v", err)
	}
	return id
}

func TestGetItemsOrdersByUsageCount(t *testing.T) {
	setupCoreTestDB(t)
	insertTestTransaction(t, "cash", "2026-01-01", "交通費", "expense", 100, -100)
	insertTestTransaction(t, "cash", "2026-01-02", "食費", "expense", 200, -300)
	insertTestTransaction(t, "cash", "2026-01-03", "食費", "expense", 300, -600)
	insertTestTransaction(t, "bank", "2026-01-04", "家賃", "expense", 1000, -1000)
	insertTestTransaction(t, "bank", "2026-01-05", "交通費", "expense", 100, -1100)

	items, err := GetItems("")
	if err != nil {
		t.Fatalf("GetItems failed: %v", err)
	}
	want := []string{"交通費", "食費", "家賃"}
	if len(items) != len(want) {
		t.Fatalf("items length = %d, want %d: %#v", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("items[%d] = %q, want %q; all=%#v", i, items[i], want[i], items)
		}
	}

	cashItems, err := GetItems("cash")
	if err != nil {
		t.Fatalf("GetItems(account) failed: %v", err)
	}
	if got, want := strings.Join(cashItems, ","), "食費,交通費"; got != want {
		t.Fatalf("cash items = %q, want %q", got, want)
	}
}
