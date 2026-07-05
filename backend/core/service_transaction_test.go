package core

import (
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
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

func TestAddTransactionRollsBackOnImageError(t *testing.T) {
	setupCoreTestDB(t)
	req := transactionRequest("cash", "2026-01-01", "食費", "expense", 1000)
	req.Images = []models.TransactionImageRequest{{
		Filename: "receipt.png",
		Data:     "不正なBase64",
		MimeType: "image/png",
	}}

	if _, err := AddTransaction(req); err == nil {
		t.Fatal("AddTransaction succeeded, want image error")
	} else if !strings.Contains(err.Error(), "画像添付エラー") {
		t.Fatalf("error = %q, want image attachment error", err)
	}

	var transactionCount, imageCount int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&transactionCount); err != nil {
		t.Fatalf("transaction count query failed: %v", err)
	}
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_images").Scan(&imageCount); err != nil {
		t.Fatalf("image count query failed: %v", err)
	}
	if transactionCount != 0 || imageCount != 0 {
		t.Fatalf("counts after rollback = transactions:%d images:%d, want 0,0", transactionCount, imageCount)
	}
}

func TestAddTransactionTagsValidatesAllTagsBeforeInsert(t *testing.T) {
	setupCoreTestDB(t)
	transactionID := insertTestTransaction(t, "cash", "2026-01-01", "食費", "expense", 1000, -1000)
	result, err := database.GetDB().Exec("INSERT INTO tags (name, level) VALUES (?, 1)", "食費")
	if err != nil {
		t.Fatalf("tag insert failed: %v", err)
	}
	validTagID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tag LastInsertId failed: %v", err)
	}

	if err := AddTransactionTags(transactionID, []int64{validTagID, 999999}); err == nil {
		t.Fatal("AddTransactionTags succeeded, want missing tag error")
	} else if !strings.Contains(err.Error(), "タグが見つかりません") {
		t.Fatalf("error = %q, want missing tag error", err)
	}

	var count int
	if err := database.GetDB().QueryRow(
		"SELECT COUNT(*) FROM transaction_tags WHERE transaction_id = ?", transactionID,
	).Scan(&count); err != nil {
		t.Fatalf("transaction tag count query failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction tag count = %d, want 0 after validation error", count)
	}
}

func TestCreateTagByPathRollsBackAllLevelsOnError(t *testing.T) {
	setupCoreTestDB(t)
	if _, err := database.GetDB().Exec(`
		CREATE TRIGGER fail_second_level_tag
		BEFORE INSERT ON tags
		WHEN NEW.level = 2
		BEGIN
			SELECT RAISE(ABORT, 'forced tag failure');
		END
	`); err != nil {
		t.Fatalf("trigger create failed: %v", err)
	}

	if _, err := CreateTagByPath("親/子"); err == nil {
		t.Fatal("CreateTagByPath succeeded, want child insert error")
	}

	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM tags").Scan(&count); err != nil {
		t.Fatalf("tag count query failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("tag count = %d, want 0 after rollback", count)
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
