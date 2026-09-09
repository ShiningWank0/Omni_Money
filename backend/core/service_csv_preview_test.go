package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

const previewLegacyCSV = "account,date,item,type,amount,memo\n" +
	"cash,2026-01-01,coffee,expense,100,identical\n" +
	"cash,2026-01-01,coffee,expense,100,changed memo\n" +
	"cash,2026-01-02,groceries,expense,500,\n"

func TestCSVPreviewLegacyAppendClassifiesNewDuplicateConflict(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "cash", Date: "2026-01-01", Item: "coffee", Type: "expense", Amount: 100, Memo: "identical",
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
	if err != nil {
		t.Fatalf("legacy preview: %v", err)
	}
	if preview.NewCount != 1 || preview.DuplicateCount != 1 || preview.ConflictCount != 1 {
		t.Fatalf("classification new=%d duplicate=%d conflict=%d, want 1/1/1", preview.NewCount, preview.DuplicateCount, preview.ConflictCount)
	}
	if preview.ReplaceImpact != nil {
		t.Fatalf("append preview must not report a replace impact: %+v", preview.ReplaceImpact)
	}
	if len(preview.SourceDigest) != 64 || len(preview.TargetDigest) != 64 {
		t.Fatalf("digest lengths source=%d target=%d", len(preview.SourceDigest), len(preview.TargetDigest))
	}
}

func TestCSVPreviewV3ReportsReplaceImpactAndDuplicates(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "card", Date: "2026-01-01", Item: "purchase", Type: "expense", Amount: 300,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := service.CreateTag("food", nil)
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := service.GetTransactions("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 {
		t.Fatalf("expected one seeded transaction, got %d", len(transactions))
	}
	if err := service.AddTransactionTags(transactions[0].ID, []int64{root.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveCreditCardSettings([]string{"card"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveBankAccountSettings([]string{"bank"}); err != nil {
		t.Fatal(err)
	}
	content, err := service.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}

	replacePreview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(content), "replace")
	if err != nil {
		t.Fatalf("v3 replace preview: %v", err)
	}
	if replacePreview.DuplicateCount != 1 || replacePreview.NewCount != 0 {
		t.Fatalf("replace classification duplicate=%d new=%d, want 1/0", replacePreview.DuplicateCount, replacePreview.NewCount)
	}
	if replacePreview.ReplaceImpact == nil {
		t.Fatal("replace preview must include the destructive impact")
	}
	impact := replacePreview.ReplaceImpact
	if impact.Transactions != 1 || impact.Tags != 1 || impact.TransactionTags != 1 || impact.LedgerSettings != 2 {
		t.Fatalf("replace impact = %+v", impact)
	}

	appendPreview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(content), "append")
	if err != nil {
		t.Fatalf("v3 append preview: %v", err)
	}
	if appendPreview.ReplaceImpact != nil {
		t.Fatalf("append preview must not report a replace impact: %+v", appendPreview.ReplaceImpact)
	}
	if appendPreview.DuplicateCount != 1 {
		t.Fatalf("append classification duplicate=%d, want 1", appendPreview.DuplicateCount)
	}
	if appendPreview.SourceDigest != replacePreview.SourceDigest {
		t.Fatal("the same payload must produce the same source digest across modes")
	}
	count, _, err := service.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(content), "replace", &CSVImportPins{
		SourceDigest: replacePreview.SourceDigest, TargetDigest: replacePreview.TargetDigest,
	})
	if err != nil || count != 1 {
		t.Fatalf("pinned replace: count=%d err=%v", count, err)
	}
}

func TestCSVApplyWithPinsDetectsTargetChange(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	preview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	pins := &CSVImportPins{SourceDigest: preview.SourceDigest, TargetDigest: preview.TargetDigest}

	// Matching pins apply cleanly.
	count, digests, err := service.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(previewLegacyCSV), "append", pins)
	if err != nil {
		t.Fatalf("apply with matching pins: %v", err)
	}
	if count != 3 {
		t.Fatalf("imported %d rows, want 3", count)
	}
	if digests.SourceDigest != preview.SourceDigest {
		t.Fatal("apply must echo the source digest of the submitted bytes")
	}

	// A changed ledger invalidates the pinned target digest.
	preview2, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
	if err != nil {
		t.Fatalf("second preview: %v", err)
	}
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "cash", Date: "2026-02-01", Item: "late entry", Type: "income", Amount: 900,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = service.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(previewLegacyCSV), "append", &CSVImportPins{
		SourceDigest: preview2.SourceDigest, TargetDigest: preview2.TargetDigest,
	})
	if !errors.Is(err, ErrCSVPreviewConflict) {
		t.Fatalf("stale target pin must fail with ErrCSVPreviewConflict, got %v", err)
	}
}

func TestCSVApplyRejectsSourceDigestMismatch(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	preview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The submitted bytes no longer match the previewed source digest.
	tampered := strings.Replace(previewLegacyCSV, "500,", "501,", 1)
	_, _, err = service.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(tampered), "append", &CSVImportPins{
		SourceDigest: preview.SourceDigest, TargetDigest: preview.TargetDigest,
	})
	if !errors.Is(err, ErrCSVPreviewConflict) {
		t.Fatalf("stale source pin must fail with ErrCSVPreviewConflict, got %v", err)
	}
}

func TestCSVPreviewRejectsLegacyReplace(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	if _, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "replace"); !errors.Is(err, ErrCSVReplaceRequiresV3) {
		t.Fatalf("legacy replace preview must stay v3-only, got %v", err)
	}
}

func TestCSVPreviewMalformedInputReturnsParserError(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	broken := "account,date,item,type,amount\n" + strings.Repeat("cash,2026-01-01,item,expense,1,\n", 3) +
		"cash,not-a-date,item,expense,1,\n"
	if _, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(broken), "append"); err == nil {
		t.Fatal("malformed rows must surface the same parser error the apply path would produce")
	}
}

func TestCSVPreviewPinsCoverReplaceDeletionSurface(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()
	service := &Service{db: db, legacy: true}
	if _, err := service.AddTransaction(models.TransactionRequest{
		Account: "cash", Date: "2026-01-01", Item: "coffee", Type: "expense", Amount: 100,
	}); err != nil {
		t.Fatal(err)
	}
	mutations := []string{
		"UPDATE transactions SET memo = NULL",
		"INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (1, 'image.png', X'01', 'image/png')",
		"DELETE FROM transaction_images; INSERT INTO transaction_images (id, transaction_id, filename, data, mime_type) VALUES (1, 1, 'image.png', X'02', 'image/png')",
		"DELETE FROM transaction_images; INSERT INTO transaction_images (id, transaction_id, filename, data, mime_type) VALUES (1, 1, 'changed.png', X'02', 'image/png')",
		"INSERT INTO transaction_image_archive (transaction_id, filename, data, mime_type) VALUES (1, '', X'01', '')",
		"UPDATE transaction_image_archive SET data = X'02'",
		"INSERT INTO transaction_archive_amounts (transaction_id, amount) VALUES (1, 9223372036854775807)",
		"INSERT INTO ai_daily_transaction_usage VALUES ('test', '2026-01-01', 1)",
	}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			preview, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			_, _, err = service.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(previewLegacyCSV), "append", &CSVImportPins{SourceDigest: preview.SourceDigest, TargetDigest: preview.TargetDigest})
			if !errors.Is(err, ErrCSVPreviewConflict) {
				t.Fatalf("changed deletion surface accepted: %v", err)
			}
		})
	}
}

func TestCSVPreviewRequiresV3Manifest(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content, err := service.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	// The parser accepts an empty archive header for inspection, but import
	// and preview both require the final completeness manifest.
	header := strings.SplitN(content, "\n", 2)[0] + "\n"
	for _, mode := range []string{"append", "replace"} {
		if _, err := service.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(header), mode); err == nil {
			t.Fatal("preview accepted missing manifest")
		}
	}
}
