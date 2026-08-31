package core

import (
	"math"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
	"omni_money/backend/validation"
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

func TestAddAndUpdateTransactionUseSharedAmountLimit(t *testing.T) {
	setupCoreTestDB(t)
	valid := transactionRequest("cash", "2026-01-01", "給与", "income", validation.MaxTransactionAmount)
	created, err := AddTransaction(valid)
	if err != nil {
		t.Fatalf("AddTransaction at limit: %v", err)
	}
	tooLarge := valid
	tooLarge.Amount = validation.MaxTransactionAmount + 1
	if _, err := AddTransaction(tooLarge); err == nil {
		t.Fatal("AddTransaction above limit succeeded")
	}
	if _, err := UpdateTransaction(created.ID, tooLarge); err == nil {
		t.Fatal("UpdateTransaction above limit succeeded")
	}
}

func TestBalanceAndAnalysisRejectInt64Overflow(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec("DROP TRIGGER validate_transactions_amount_insert"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA ignore_check_constraints = ON"); err != nil {
		t.Fatal(err)
	}
	for index, amount := range []int64{math.MaxInt64, 1} {
		date := []string{"2026-01-01", "2026-01-02"}[index]
		if _, err := db.Exec(
			`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('overflow', ?, 'item', 'income', ?, 0)`,
			date, amount,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := recalculateBalance("overflow"); err == nil {
		t.Fatal("recalculateBalance overflow succeeded")
	}
	if _, err := AnalyzeTransactions(models.AnalysisRequest{Account: "overflow"}); err == nil {
		t.Fatal("AnalyzeTransactions overflow succeeded")
	}
}

func TestCrossAccountAggregatesUseCheckedArithmetic(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec("DROP TRIGGER validate_transactions_amount_insert"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA ignore_check_constraints = ON"); err != nil {
		t.Fatal(err)
	}
	tagID := insertTagSummaryTestTag(t, "overflow", nil, 1)
	for index, amount := range []int64{math.MaxInt64, 1} {
		result, err := db.Exec(
			`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES (?, ?, 'item', 'income', ?, ?)`,
			[]string{"first", "second"}[index],
			[]string{"2026-01-01", "2026-01-02"}[index],
			amount,
			amount,
		)
		if err != nil {
			t.Fatal(err)
		}
		transactionID, _ := result.LastInsertId()
		linkAnalysisTestTag(t, transactionID, tagID)
	}
	if _, err := AnalyzeTransactions(models.AnalysisRequest{}); err == nil || !strings.Contains(err.Error(), "分析集計オーバーフロー") {
		t.Fatalf("cross-account analysis overflow = %v", err)
	}
	if _, err := GetTagSummary("income", "", ""); err == nil || !strings.Contains(err.Error(), "タグ直接金額集計オーバーフロー") {
		t.Fatalf("cross-account tag overflow = %v", err)
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
