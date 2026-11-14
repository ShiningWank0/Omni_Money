package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func removeSubscriptionSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for index := len(subscriptionSchema) - 1; index >= 0; index-- {
		object := subscriptionSchema[index]
		if _, err := db.Exec("DROP " + object.kind + " " + object.name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVersionSixMigratesSubscriptionsWithoutChangingManualTransactions(t *testing.T) {
	instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	removeSubscriptionSchemaForTest(t, instance.DB())
	if _, err := instance.DB().Exec("INSERT INTO transactions(account,date,item,type,amount,balance,memo) VALUES('cash','2026-01-31','manual','expense',123,456,'same'); PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	if err := createTablesOn(instance.DB()); err != nil {
		t.Fatal(err)
	}
	if err := validateSubscriptionSchema(context.Background(), instance.DB()); err != nil {
		t.Fatal(err)
	}
	var amount, balance, count int
	if err := instance.DB().QueryRow("SELECT amount,balance FROM transactions WHERE item='manual'").Scan(&amount, &balance); err != nil {
		t.Fatal(err)
	}
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM subscription_occurrences").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if amount != 123 || balance != 456 || count != 0 {
		t.Fatalf("amount=%d balance=%d occurrences=%d", amount, balance, count)
	}
}

func TestSubscriptionSchemaRejectsTamperingAndNewerVersion(t *testing.T) {
	for _, statement := range []string{"DROP INDEX idx_subscription_due", "PRAGMA user_version=8", "DROP TABLE subscription_dismissals; CREATE TABLE subscription_dismissals(fingerprint TEXT PRIMARY KEY)"} {
		t.Run(statement, func(t *testing.T) {
			instance, err := OpenPlainInstance(filepath.Join(t.TempDir(), "ledger.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Close()
			if _, err := instance.DB().Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err := createTablesOn(instance.DB()); err == nil {
				t.Fatal("tampered schema accepted")
			}
		})
	}
}
