package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestBankAccountSettingsRoundTrip(t *testing.T) {
	setupCoreTestDB(t)

	if err := SaveBankAccountSettings([]string{"main bank", "savings"}); err != nil {
		t.Fatalf("SaveBankAccountSettings failed: %v", err)
	}
	waitForSnapshotCount(t, 1)
	items, err := GetBankAccountSettings()
	if err != nil {
		t.Fatalf("GetBankAccountSettings failed: %v", err)
	}
	if got, want := strings.Join(items, ","), "main bank,savings"; got != want {
		t.Fatalf("bank account settings = %q, want %q", got, want)
	}
}

func TestInvalidSettingJSONReturnsErrorAndStopsPruning(t *testing.T) {
	setupCoreTestDB(t)
	cardTx := insertTestTransaction(t, "credit card", "2026-01-01", "食費", "expense", 1000, -1000)
	bankTx := insertTestTransaction(t, "main bank", "2026-01-27", "カード引き落とし", "expense", 1000, -1000)
	if _, err := database.GetDB().Exec(
		"INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)", cardTx, bankTx,
	); err != nil {
		t.Fatalf("transaction link insert failed: %v", err)
	}
	if _, err := database.GetDB().Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?)", "credit_card_items", "not-json",
	); err != nil {
		t.Fatalf("invalid setting insert failed: %v", err)
	}

	if _, err := GetCreditCardSettings(); err == nil {
		t.Fatal("GetCreditCardSettings succeeded, want JSON error")
	}
	if err := pruneInvalidTransactionLinks(); err == nil {
		t.Fatal("pruneInvalidTransactionLinks succeeded, want settings error")
	}

	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&count); err != nil {
		t.Fatalf("transaction link count query failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("transaction link count = %d, want 1 after aborted prune", count)
	}
}

func TestAddTransactionLinkRequiresCreditCardAndBankPair(t *testing.T) {
	setupCoreTestDB(t)
	cardTx := insertTestTransaction(t, "credit card", "2026-01-01", "食費", "expense", 1000, -1000)
	bankTx := insertTestTransaction(t, "main bank", "2026-01-27", "カード引き落とし", "expense", 1000, -1000)
	cashTx := insertTestTransaction(t, "cash", "2026-01-02", "交通費", "expense", 200, -200)

	writeStringSliceSetting(t, "credit_card_items", []string{"credit card"})
	writeStringSliceSetting(t, "bank_account_items", []string{"main bank"})

	if err := AddTransactionLink(cardTx, cashTx); err == nil {
		t.Fatal("AddTransactionLink(card,cash) succeeded, want error")
	}
	if err := AddTransactionLink(cardTx, bankTx); err != nil {
		t.Fatalf("AddTransactionLink(card,bank) failed: %v", err)
	}
	waitForSnapshotCount(t, 1)
}

func TestUpdateTransactionPrunesInvalidTransactionLinks(t *testing.T) {
	setupCoreTestDB(t)
	cardTx := insertTestTransaction(t, "credit card", "2026-01-01", "食費", "expense", 1000, -1000)
	bankTx := insertTestTransaction(t, "main bank", "2026-01-27", "カード引き落とし", "expense", 1000, -1000)
	writeStringSliceSetting(t, "credit_card_items", []string{"credit card"})
	writeStringSliceSetting(t, "bank_account_items", []string{"main bank"})

	if err := AddTransactionLink(cardTx, bankTx); err != nil {
		t.Fatalf("AddTransactionLink(card,bank) failed: %v", err)
	}

	if _, err := UpdateTransaction(cardTx, transactionRequest("cash", "2026-01-01", "食費", "expense", 1000)); err != nil {
		t.Fatalf("UpdateTransaction failed: %v", err)
	}
	waitForSnapshotCount(t, 2)

	links, err := GetTransactionLinks(bankTx)
	if err != nil {
		t.Fatalf("GetTransactionLinks failed: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links length = %d, want 0: %#v", len(links), links)
	}
}

func TestSaveBankAccountSettingsPrunesInvalidTransactionLinks(t *testing.T) {
	setupCoreTestDB(t)
	cardTx := insertTestTransaction(t, "credit card", "2026-01-01", "食費", "expense", 1000, -1000)
	bankTx := insertTestTransaction(t, "main bank", "2026-01-27", "カード引き落とし", "expense", 1000, -1000)
	writeStringSliceSetting(t, "credit_card_items", []string{"credit card"})
	writeStringSliceSetting(t, "bank_account_items", []string{"main bank"})

	if err := AddTransactionLink(cardTx, bankTx); err != nil {
		t.Fatalf("AddTransactionLink(card,bank) failed: %v", err)
	}
	if err := SaveBankAccountSettings([]string{"other bank"}); err != nil {
		t.Fatalf("SaveBankAccountSettings failed: %v", err)
	}
	waitForSnapshotCount(t, 2)

	links, err := GetTransactionLinks(cardTx)
	if err != nil {
		t.Fatalf("GetTransactionLinks failed: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links length = %d, want 0: %#v", len(links), links)
	}
}

func transactionRequest(account, date, item, txType string, amount int64) models.TransactionRequest {
	return models.TransactionRequest{
		Account: account,
		Date:    date,
		Item:    item,
		Type:    txType,
		Amount:  amount,
	}
}

func writeStringSliceSetting(t *testing.T, key string, items []string) {
	t.Helper()
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if _, err := database.GetDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, string(data)); err != nil {
		t.Fatalf("setting insert failed: %v", err)
	}
}

func waitForSnapshotCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, err := database.ListSnapshots("")
		if err == nil && len(snapshots) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapshots, _ := database.ListSnapshots("")
	t.Fatalf("snapshot count = %d, want at least %d", len(snapshots), want)
}
