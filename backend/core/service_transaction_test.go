package core

import (
	"strings"
	"testing"

	"omni_money/backend/database"
)

func TestUpdateTransactionRejectsMissingID(t *testing.T) {
	setupCoreTestDB(t)

	_, err := UpdateTransaction(999999, transactionRequest("cash", "2026-01-01", "食費", "expense", 1000))
	if err == nil {
		t.Fatal("UpdateTransaction succeeded, want error")
	}
	if !strings.Contains(err.Error(), "取引が見つかりません") {
		t.Fatalf("error = %q, want missing transaction error", err)
	}
}

func TestAddTransactionRollsBackOnTagError(t *testing.T) {
	setupCoreTestDB(t)
	req := transactionRequest("cash", "2026-01-01", "食費", "expense", 1000)
	req.Tags = []int64{999999}

	if _, err := AddTransaction(req); err == nil {
		t.Fatal("AddTransaction succeeded, want tag error")
	}

	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatalf("transaction count query failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction count = %d, want 0 after rollback", count)
	}
}

func TestUpdateTransactionRollsBackOnTagError(t *testing.T) {
	setupCoreTestDB(t)
	id := insertTestTransaction(t, "cash", "2026-01-01", "食費", "expense", 1000, -1000)
	req := transactionRequest("bank", "2026-02-01", "給与", "income", 2000)
	req.Tags = []int64{999999}

	if _, err := UpdateTransaction(id, req); err == nil {
		t.Fatal("UpdateTransaction succeeded, want tag error")
	}

	var account, date, item, txType string
	var amount int64
	if err := database.GetDB().QueryRow(
		"SELECT account, date, item, type, amount FROM transactions WHERE id = ?", id,
	).Scan(&account, &date, &item, &txType, &amount); err != nil {
		t.Fatalf("transaction query failed: %v", err)
	}
	if account != "cash" || !strings.HasPrefix(date, "2026-01-01") || item != "食費" || txType != "expense" || amount != 1000 {
		t.Fatalf("transaction changed after rollback: account=%q date=%q item=%q type=%q amount=%d", account, date, item, txType, amount)
	}
}
