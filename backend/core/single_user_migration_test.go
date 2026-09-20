package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

// Runbook fixture: only a copy of a stopped source is opened, read-only.
// SQLCipher and old schema conversion have dedicated integration suites;
// this test exercises the CSV handoff between independent ledger instances.
func TestSingleUserMigrationReadOnlyCopyToIndependentLedger(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.db")
	source, err := database.OpenPlainInstance(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(source)
	if err != nil {
		t.Fatal(err)
	}
	card, err := service.AddTransaction(models.TransactionRequest{Account: "card", Date: "2026-01-01", Item: "receipt", Type: "expense", Amount: 500, Memo: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	bank, err := service.AddTransaction(models.TransactionRequest{Account: "bank", Date: "2026-01-02", Item: "payment", Type: "expense", Amount: 500})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := service.CreateTag("food", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTag("coffee", &tag.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		service.AddTransactionTags(card.ID, []int64{child.ID}),
		service.SaveCreditCardSettings([]string{"card"}), service.SaveBankAccountSettings([]string{"bank"}),
		service.AddTransactionLink(card.ID, bank.ID),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AddTransactionImage(card.ID, models.TransactionImageRequest{Filename: "receipt.png", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(encodePNG(t))}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB().Exec("INSERT INTO settings VALUES ('legacy_auth_secret', 'never-transfer')"); err != nil {
		t.Fatal(err)
	}
	// Seed AI-only state as well: without a real row the "AI settings are not
	// migrated" assertion would hold vacuously even if a regression started
	// exporting it. A 32-byte key/request pair satisfies the table CHECK.
	if _, err := source.DB().Exec(`INSERT INTO ai_transaction_idempotency
		(credential_id, idempotency_key_sha256, request_sha256, transaction_id, response_account, response_date, created_at)
		VALUES ('legacy-ai-cred', ?, ?, NULL, NULL, NULL, '2026-01-01T00:00:00Z')`,
		make([]byte, 32), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256(original)
	copyPath := filepath.Join(root, "copy.db")
	if err := os.WriteFile(copyPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	copyDB, err := sql.Open("sqlite3", "file:"+copyPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if _, err := copyDB.Exec("DELETE FROM transactions"); err == nil {
		t.Fatal("copy was writable")
	}
	var integrity string
	if err := copyDB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("source integrity: %s %v", integrity, err)
	}
	readonly := &Service{db: copyDB, legacy: true}
	content, err := readonly.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "never-transfer") {
		t.Fatal("legacy auth setting escaped into CSV")
	}
	if strings.Contains(content, "legacy-ai-cred") {
		t.Fatal("legacy AI idempotency state escaped into CSV")
	}
	target, targetService := openCoreTestService(t, "new-user-vault")
	for attempt := 0; attempt < 2; attempt++ {
		if count, err := targetService.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "replace"); err != nil || count != 2 {
			t.Fatalf("import attempt %d: %d %v", attempt, count, err)
		}
		for query, want := range map[string]int{
			"SELECT COUNT(*) FROM transactions":       2,
			"SELECT COUNT(*) FROM tags":               2,
			"SELECT COUNT(*) FROM transaction_tags":   1,
			"SELECT COUNT(*) FROM transaction_images": 1,
			"SELECT COUNT(*) FROM transaction_links l JOIN transactions p ON p.id=l.parent_id JOIN transactions c ON c.id=l.child_id WHERE p.account='card' AND c.account='bank'": 1,
			"SELECT COUNT(*) FROM settings WHERE key IN ('credit_card_items','bank_account_items')":                                                                               2,
			"SELECT COUNT(*) FROM settings WHERE key='legacy_auth_secret'":                                                                                                        0,
			"SELECT COUNT(*) FROM ai_transaction_idempotency":                                                                                                                     0,
		} {
			var got int
			if err := target.DB().QueryRow(query).Scan(&got); err != nil || got != want {
				t.Fatalf("handoff invariant: got %d want %d err %v", got, want, err)
			}
		}
		var data []byte
		if err := target.DB().QueryRow("SELECT data FROM transaction_images").Scan(&data); err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(data) != sha256.Sum256(encodePNG(t)) {
			t.Fatal("image changed")
		}
	}
	for _, path := range []string{sourcePath, copyPath} {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != originalHash {
			t.Fatalf("source changed: %v", err)
		}
	}
}
