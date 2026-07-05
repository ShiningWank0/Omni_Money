package core

import (
	"strings"
	"testing"

	"omni_money/backend/database"
)

func TestImportCSVAcceptsLegacyBackupFormat(t *testing.T) {
	setupCoreTestDB(t)
	legacyCSV := `id,account,date,item,type,amount,balance
101,cash,2026-01-01,給与,income,1000,999999
102,cash,2026-01-02,食費,expense,300,123456
`

	imported, err := ImportCSV(legacyCSV, "replace")
	if err != nil {
		t.Fatalf("ImportCSV failed: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d, want 2", imported)
	}
	waitForSnapshotCount(t, 1)

	transactions, err := GetTransactions("cash", "")
	if err != nil {
		t.Fatalf("GetTransactions failed: %v", err)
	}
	if len(transactions) != 2 {
		t.Fatalf("transactions length = %d, want 2: %#v", len(transactions), transactions)
	}
	if transactions[0].ID == 101 || transactions[1].ID == 102 {
		t.Fatalf("legacy CSV IDs were reused: %#v", transactions)
	}
	if transactions[0].Balance != 1000 || transactions[1].Balance != 700 {
		t.Fatalf("balances = %d,%d; want 1000,700", transactions[0].Balance, transactions[1].Balance)
	}
	if transactions[0].Memo != "" || transactions[1].Memo != "" {
		t.Fatalf("memo values = %q,%q; want empty", transactions[0].Memo, transactions[1].Memo)
	}
}

func TestImportCSVAcceptsLegacyDateOnlyAndDateTime(t *testing.T) {
	setupCoreTestDB(t)
	legacyCSV := `id,account,date,item,type,amount,balance
1,cash,2026-01-01,給与,income,1000,1000
2,cash,2026-01-02 12:34:56,食費,expense,300,700
`

	imported, err := ImportCSV(legacyCSV, "replace")
	if err != nil {
		t.Fatalf("ImportCSV failed: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d, want 2", imported)
	}
	waitForSnapshotCount(t, 1)

	transactions, err := GetTransactions("cash", "")
	if err != nil {
		t.Fatalf("GetTransactions failed: %v", err)
	}
	if got, want := transactions[0].Date, "2026-01-01"; got != want {
		t.Fatalf("date-only transaction date = %q, want %q", got, want)
	}
	if got, want := transactions[1].Date, "2026-01-02 12:34:56"; got != want {
		t.Fatalf("date-time transaction date = %q, want %q", got, want)
	}
}

func TestImportCSVRejectsInvalidRowsWithRowNumber(t *testing.T) {
	tests := []struct {
		name          string
		invalidRecord string
		wantMessage   string
	}{
		{name: "不正金額", invalidRecord: "cash,2026-01-02,食費,expense,12abc", wantMessage: "金額は正の整数"},
		{name: "ゼロ金額", invalidRecord: "cash,2026-01-02,食費,expense,0", wantMessage: "金額は正の整数"},
		{name: "不正種別", invalidRecord: "cash,2026-01-02,食費,transfer,300", wantMessage: "種別はincomeまたはexpense"},
		{name: "不正日付", invalidRecord: "cash,2026-02-30,食費,expense,300", wantMessage: "日付形式が正しくありません"},
		{name: "空口座", invalidRecord: ",2026-01-02,食費,expense,300", wantMessage: "口座名は必須"},
		{name: "空項目", invalidRecord: "cash,2026-01-02,,expense,300", wantMessage: "項目は必須"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupCoreTestDB(t)
			originalID := insertTestTransaction(t, "bank", "2025-12-31", "繰越", "income", 500, 500)
			content := "account,date,item,type,amount\n" +
				"cash,2026-01-01,給与,income,1000\n" +
				tt.invalidRecord + "\n"

			imported, err := ImportCSV(content, "replace")
			if err == nil {
				t.Fatal("ImportCSV succeeded, want error")
			}
			if imported != 0 {
				t.Fatalf("imported = %d, want 0", imported)
			}
			if !strings.Contains(err.Error(), "行3") || !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("error = %q, want row number and %q", err, tt.wantMessage)
			}

			var count int
			if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions WHERE id = ?", originalID).Scan(&count); err != nil {
				t.Fatalf("transaction count query failed: %v", err)
			}
			if count != 1 {
				t.Fatalf("original transaction count = %d, want 1 after rollback", count)
			}
		})
	}
}
