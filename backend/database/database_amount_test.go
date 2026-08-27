package database

import (
	"path/filepath"
	"testing"
)

func TestTransactionAmountTriggersProtectExistingSchema(t *testing.T) {
	if err := InitDB(filepath.Join(t.TempDir(), "amount.db")); err != nil {
		t.Fatal(err)
	}
	defer CloseDB()

	for _, amount := range []int64{0, -1, 1_000_000_001} {
		if _, err := GetDB().Exec(
			`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2026-01-01','item','income',?,0)`,
			amount,
		); err == nil {
			t.Errorf("INSERT amount %d succeeded", amount)
		}
	}
	result, err := GetDB().Exec(
		`INSERT INTO transactions (account, date, item, type, amount, balance) VALUES ('cash','2026-01-01','item','income',1000000000,0)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if _, err := GetDB().Exec("UPDATE transactions SET amount = 1000000001 WHERE id = ?", id); err == nil {
		t.Fatal("UPDATE above amount limit succeeded")
	}
}
