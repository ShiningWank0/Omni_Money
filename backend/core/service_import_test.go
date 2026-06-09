package core

import "testing"

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
