package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/securedb"
)

// Exercise public mutation paths while another connection owns the writer.
// A deferred transaction used to read successfully and then fail immediately
// on its first write, despite the database's five-second busy timeout.
func TestLedgerMutationsWaitForCompetingWriter(t *testing.T) {
	testLedgerMutationsWaitForCompetingWriter(t, false)
}

func TestEncryptedLedgerMutationsWaitForCompetingWriter(t *testing.T) {
	testLedgerMutationsWaitForCompetingWriter(t, true)
}

func testLedgerMutationsWaitForCompetingWriter(t *testing.T, encrypted bool) {
	for _, name := range []string{
		"transaction-create", "transaction-update", "transaction-delete", "balance",
		"settings", "tag-create", "tag-update", "tag-delete", "transaction-tags",
		"image", "save-receipt", "save-worker", "csv-legacy-pinned", "csv-v3-pinned",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.db")
			var instance *database.Instance
			var err error
			if encrypted {
				instance, err = database.OpenEncryptedInstance(path, securedb.RawKey{0: 0x71, 31: 0x92})
				if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" &&
					(errors.Is(err, securedb.ErrCipherUnavailable) || errors.Is(err, securedb.ErrCipherVersion) || errors.Is(err, securedb.ErrCipherProvider)) {
					t.Skipf("SQLCipher integration build is not active: %v", err)
				}
			} else {
				instance, err = database.OpenPlainInstance(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = instance.Close() })
			// No background worker or automatic snapshot: the test controls which
			// writer runs, while using the normal service/database implementation.
			s := &Service{instance: instance, db: instance.DB()}
			request := saveTestRequest()
			transaction, err := s.AddTransaction(request)
			if err != nil {
				t.Fatal(err)
			}
			tag, err := s.CreateTag("existing", nil)
			if err != nil {
				t.Fatal(err)
			}
			var operation func() error
			switch name {
			case "transaction-create":
				request.NewTagPaths = []string{"new/child"}
				operation = func() error { _, err := s.AddTransaction(request); return err }
			case "transaction-update":
				request.Amount++
				operation = func() error { _, err := s.UpdateTransaction(transaction.ID, request); return err }
			case "transaction-delete":
				operation = func() error { return s.DeleteTransaction(transaction.ID) }
			case "balance":
				operation = func() error { return s.recalculateBalance(request.Account) }
			case "settings":
				operation = func() error { return s.SaveBankAccountSettings([]string{"bank"}) }
			case "tag-create":
				operation = func() error { _, err := s.CreateTag("child", &tag.ID); return err }
			case "tag-update":
				operation = func() error { return s.UpdateTag(tag.ID, "renamed") }
			case "tag-delete":
				operation = func() error { return s.DeleteTag(tag.ID) }
			case "transaction-tags":
				operation = func() error { return s.AddTransactionTags(transaction.ID, []int64{tag.ID}) }
			case "image":
				image := imageRequest("receipt.png", "image/png", encodePNG(t))
				operation = func() error { _, err := s.AddTransactionImage(transaction.ID, image); return err }
			case "save-receipt", "save-worker":
				if name == "save-receipt" {
					operation = func() error { _, err := s.AcceptTransactionSave(saveTestID, 0, request); return err }
				} else {
					if _, err := s.AcceptTransactionSave(saveTestID, 0, request); err != nil {
						t.Fatal(err)
					}
					payload, err := json.Marshal(transactionSavePayload{Transaction: request})
					if err != nil {
						t.Fatal(err)
					}
					operation = func() error { return s.processTransactionSave(saveTestID, payload) }
				}
			case "csv-legacy-pinned", "csv-v3-pinned":
				content := previewLegacyCSV
				if name == "csv-v3-pinned" {
					content, err = s.BackupToCSV()
					if err != nil {
						t.Fatal(err)
					}
				}
				preview, err := s.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(content), "append")
				if err != nil {
					t.Fatal(err)
				}
				operation = func() error {
					_, _, err := s.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(content), "append",
						&CSVImportPins{SourceDigest: preview.SourceDigest, TargetDigest: preview.TargetDigest})
					return err
				}
			}
			assertLedgerWriterWaits(t, instance.DB(), operation)
		})
	}
}

func assertLedgerWriterWaits(t *testing.T, db *sql.DB, operation func() error) {
	t.Helper()
	blocker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := blocker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.ExecContext(context.Background(), "ROLLBACK")
	done := make(chan error, 1)
	go func() { done <- operation() }()
	received := false
	defer func() {
		_, _ = blocker.ExecContext(context.Background(), "ROLLBACK")
		if !received {
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Error("writer did not finish after rollback")
			}
		}
	}()
	select {
	case err := <-done:
		received = true
		t.Fatalf("operation returned before competing writer released its lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := blocker.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		received = true
		if err != nil {
			t.Fatalf("operation failed after competing writer committed: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("operation did not finish after competing writer committed")
	}
}

func TestLedgerReadTransactionsProceedDuringCompetingWriter(t *testing.T) {
	instance, s := openCoreTestService(t, "reader-with-writer")
	blocker, err := instance.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := blocker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.BackupToCSVFullContext(ctx); err != nil {
		t.Fatalf("read-only export waited for writer: %v", err)
	}
	if _, err := s.PreviewCSVImportReaderContext(ctx, strings.NewReader(previewLegacyCSV), "append"); err != nil {
		t.Fatalf("read-only preview waited for writer: %v", err)
	}
}

func TestLedgerWriteCancellationReturnsConnection(t *testing.T) {
	instance, _ := openCoreTestService(t, "cancelled-writer")
	db := instance.DB()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := beginLedgerWrite(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO tags(name,level) VALUES('cancelled',1)"); err != nil {
		t.Fatal(err)
	}
	cancel()
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var count int
	if err := db.QueryRowContext(check, "SELECT COUNT(*) FROM tags WHERE name='cancelled'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled operation left tags or retained connection: count=%d, err=%v", count, err)
	}
	next, err := beginLedgerWrite(check, db)
	if err != nil {
		t.Fatalf("cancelled transaction retained writer: %v", err)
	}
	if err := next.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedCSVImportRechecksLedgerAfterWaitingForWriter(t *testing.T) {
	instance, s := openCoreTestService(t, "pinned-writer")
	preview, err := s.PreviewCSVImportReaderContext(context.Background(), strings.NewReader(previewLegacyCSV), "append")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := instance.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := blocker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.ExecContext(context.Background(), "ROLLBACK")
	if _, err := blocker.ExecContext(context.Background(), "INSERT INTO settings(key,value) VALUES('bank_account_items','[\"bank\"]')"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.ImportCSVReaderContextWithDigests(context.Background(), strings.NewReader(previewLegacyCSV), "append",
			&CSVImportPins{SourceDigest: preview.SourceDigest, TargetDigest: preview.TargetDigest})
		done <- err
	}()
	received := false
	defer func() {
		_, _ = blocker.ExecContext(context.Background(), "ROLLBACK")
		if !received {
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Error("pinned import did not finish after rollback")
			}
		}
	}()
	select {
	case err := <-done:
		received = true
		t.Fatalf("pinned import returned before writer committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := blocker.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		received = true
		if !errors.Is(err, ErrCSVPreviewConflict) {
			t.Fatalf("changed ledger error = %v, want preview conflict", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("pinned import did not finish")
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale preview changed ledger: transactions=%d, err=%v", count, err)
	}
}
