package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
	"omni_money/backend/validation"
)

func TestCSVTempBudgetIsSharedAcrossUploadAndExportReservations(t *testing.T) {
	first, ok := TryAcquireCSVTempBudget(MaxCSVImportWireBytes)
	if !ok {
		t.Fatal("first CSV temp reservation failed")
	}
	second, ok := TryAcquireCSVTempBudget(MaxCSVImportWireBytes)
	if !ok {
		first()
		t.Fatal("second CSV temp reservation failed")
	}
	if _, ok := TryAcquireCSVTempBudget(MaxCSVImportBytes); ok {
		first()
		second()
		t.Fatal("third full-size CSV temp reservation unexpectedly succeeded")
	}
	first()
	third, ok := TryAcquireCSVTempBudget(MaxCSVImportBytes)
	if !ok {
		second()
		t.Fatal("released CSV temp reservation was not reusable")
	}
	third()
	second()
}

func TestCSVTempBudgetAccountsForDecodedImageWorkingCopy(t *testing.T) {
	rawRelease, ok := TryAcquireCSVTempBudget(MaxCSVImportWireBytes)
	if !ok {
		t.Fatal("raw CSV reservation failed")
	}
	defer rawRelease()
	decodedRelease, ok := TryAcquireCSVTempBudget(models.MaxImageBytesDatabase * 2)
	if !ok {
		t.Fatal("raw plus decoded-image working reservation failed")
	}
	defer decodedRelease()
	if _, ok := TryAcquireCSVTempBudget(MaxCSVTempBudgetBytes - MaxCSVImportWireBytes - models.MaxImageBytesDatabase*2 + 1); ok {
		t.Fatal("CSV temp budget allowed bytes beyond raw plus decoded image peak")
	}
}

func TestCSVTempBudgetReservationsAreAtomicUnderConcurrency(t *testing.T) {
	start := make(chan struct{})
	var wait sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			release, ok := TryAcquireCSVTempBudget(MaxCSVImportWireBytes)
			if !ok {
				return
			}
			mu.Lock()
			releases = append(releases, release)
			mu.Unlock()
		}()
	}
	close(start)
	wait.Wait()
	if len(releases) != 2 {
		for _, release := range releases {
			release()
		}
		t.Fatalf("concurrent full reservations = %d, want exactly 2", len(releases))
	}
	for _, release := range releases {
		release()
	}
}

func TestCSVReaderLegacyAppendAllowsExtraRecordTypeColumn(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := "id,account,date,item,type,amount,balance,memo,omni_money_csv_version,record_type\n" +
		"101,cash,2026-01-01,給与,income,1000,1000,,2,legacy-note\n"
	if imported, err := service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append"); err != nil || imported != 1 {
		t.Fatalf("legacy reader with extra record_type result = %d, %v; want append success", imported, err)
	}
}

func TestCSVReaderDoesNotRouteLegacyExtraRecordTypeByFirstRowVersion(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	// A legacy/v2 producer may add an application column called record_type.
	// Even when a crafted first row contains "3", the non-official header must
	// stay on the legacy schema instead of entering the v3 parser with missing
	// columns and (in replace mode) a destructive interpretation.
	content := "id,account,date,item,type,amount,balance,memo,omni_money_csv_version,record_type,filename\n" +
		"101,cash,2026-01-01,給与,income,1000,1000,,3,transaction,receipt.csv\n"
	if imported, err := service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append"); err == nil || !strings.Contains(err.Error(), "未対応のCSVバージョン") {
		t.Fatalf("legacy-shaped version-3 CSV result = %d, %v; want legacy version error", imported, err)
	}
}

func TestBackupToCSVDefaultAlwaysEmitsV3AndV2IsExplicit(t *testing.T) {
	setupCoreTestDB(t)
	insertTestTransaction(t, "cash", "2026-08-01", "salary", "income", 100, 100)

	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || len(records[0]) != len(csvV3Headers) || records[0][0] != csvVersionHeader || records[1][0] != csvVersion3 || records[2][1] != csvV3ManifestRecordType {
		t.Fatalf("default backup was not v3: %#v", records)
	}

	legacyContent, err := BackupToCSVV2()
	if err != nil {
		t.Fatal(err)
	}
	legacyRecords, err := csv.NewReader(strings.NewReader(legacyContent)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyRecords) != 2 || len(legacyRecords[0]) != 9 || legacyRecords[1][8] != csvVersion2 {
		t.Fatalf("explicit v2 backup = %#v", legacyRecords)
	}
}

func TestCSVFieldLimitReaderHandlesQuotedRecordsBeforeCSVAllocation(t *testing.T) {
	input := "account,detail\n" + "cash,\"line one\nline two with \"\"quotes\"\"\"\n"
	guarded := &csvFieldLimitReader{ctx: context.Background(), input: strings.NewReader(input), maxFieldBytes: 64, fieldStart: true}
	records, err := csv.NewReader(guarded).ReadAll()
	if err != nil {
		t.Fatalf("quoted multiline CSV rejected: %v", err)
	}
	if len(records) != 2 || records[1][1] != "line one\nline two with \"quotes\"" {
		t.Fatalf("quoted CSV records = %#v", records)
	}
	crlf := &csvFieldLimitReader{ctx: context.Background(), input: strings.NewReader("a,\"x\r\ny\"\n"), maxFieldBytes: 3, fieldStart: true}
	if records, err := csv.NewReader(crlf).ReadAll(); err != nil || len(records) != 1 || records[0][1] != "x\ny" {
		t.Fatalf("quoted CRLF normalization failed: records=%#v err=%v", records, err)
	}
	lineCRLF := &csvFieldLimitReader{ctx: context.Background(), input: strings.NewReader("abcd\r\n"), maxFieldBytes: 4, fieldStart: true}
	if records, err := csv.NewReader(lineCRLF).ReadAll(); err != nil || len(records) != 1 || records[0][0] != "abcd" {
		t.Fatalf("record CRLF counted as field data: records=%#v err=%v", records, err)
	}

	giant := "account,detail\n,\"" + strings.Repeat("x", maxCSVFieldBytes+1) + "\"\n"
	guarded = &csvFieldLimitReader{ctx: context.Background(), input: strings.NewReader(giant), maxFieldBytes: maxCSVFieldBytes, fieldStart: true}
	if _, err := csv.NewReader(guarded).ReadAll(); err == nil || !strings.Contains(err.Error(), "列が大きすぎます") {
		t.Fatalf("giant quoted field result = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	guarded = &csvFieldLimitReader{ctx: ctx, input: strings.NewReader("a,b\n"), maxFieldBytes: 16, fieldStart: true}
	if _, err := guarded.Read(make([]byte, 32)); err == nil {
		t.Fatal("canceled CSV reader unexpectedly read input")
	}
	aggregate := "a,b\n\"" + strings.Repeat("x", maxCSVGuardFieldBytes) + "\",\"" + strings.Repeat("y", maxCSVGuardFieldBytes) + "\"\n"
	guarded = &csvFieldLimitReader{ctx: context.Background(), input: strings.NewReader(aggregate), maxFieldBytes: maxCSVGuardFieldBytes, fieldStart: true}
	aggregateReader := csv.NewReader(guarded)
	if _, err := aggregateReader.Read(); err != nil {
		t.Fatalf("aggregate CSV header failed: %v", err)
	}
	if _, err := aggregateReader.Read(); err == nil || !strings.Contains(err.Error(), "レコード") {
		t.Fatalf("aggregate CSV record was not bounded before allocation: %v", err)
	}
}

func TestCSVV3RejectsUnframedQuotedCRLFInsteadOfNormalizingIt(t *testing.T) {
	row := csvV3Record(map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash",
		"date": "2026-08-01", "item": "item", "type": "income", "amount": "1",
		"memo": "line one\r\nline two",
	})
	content := writeCSVRecordsForTest(t, [][]string{csvV3Headers, row})
	if _, err := (&Service{}).parseCSVV3Reader(context.Background(), strings.NewReader(content), false); err == nil || !strings.Contains(err.Error(), "lossless") {
		t.Fatalf("unframed quoted CRLF result = %v", err)
	}
}

func TestCSVV3ParseErrorCleansImageSpoolWhenResultIsZero(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
		map[string]string{csvVersionHeader: "3", "record_type": "unknown"},
	)
	before := csvImageSpoolDirectories(t)
	if _, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true); err == nil {
		t.Fatal("unknown record unexpectedly parsed")
	}
	after := csvImageSpoolDirectories(t)
	for name := range after {
		if _, existed := before[name]; !existed {
			t.Fatalf("image spool directory leaked after parse error: %s", name)
		}
	}
	// The zero-value error result must not lose the reservations owned by the
	// parser. A full-budget reservation is a simple observable proof that the
	// image admission was released on every cleanup path.
	release, ok := TryAcquireCSVTempBudget(MaxCSVTempBudgetBytes)
	if !ok {
		t.Fatal("CSV temp budget leaked after parse error")
	}
	release()
}

func csvImageSpoolDirectories(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]struct{})
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "omni-money-csv-images-") && entry.IsDir() {
			result[entry.Name()] = struct{}{}
		}
	}
	return result
}

func TestCSVV3ImageAdmissionHappensBeforeBase64Decode(t *testing.T) {
	reserved, ok := TryAcquireCSVTempBudget(MaxCSVTempBudgetBytes)
	if !ok {
		t.Fatal("could not reserve CSV budget for admission test")
	}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	if _, err := (&Service{}).parseCSVV3(content); err == nil || !strings.Contains(err.Error(), "一時領域") {
		reserved()
		t.Fatalf("image was decoded without an admission reservation: %v", err)
	}
	reserved()
	parsed, err := (&Service{}).parseCSVV3(content)
	if err != nil {
		t.Fatalf("image parse after reservation release failed: %v", err)
	}
	parsed.cleanup()
}

func TestCSVV3ImageSpoolRejectsReplacementAfterValidation(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	parsed, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	path := parsed.images[0].dataPath
	replacement := path + ".replacement"
	if err := os.Rename(path, replacement); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.Rename(replacement, path)
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("substituted"))
	_ = f.Close()
	if _, err := service.importCSVV3Parsed(context.Background(), &parsed, "replace"); err == nil {
		t.Fatal("replacement image was accepted")
	}
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("original spool was lost: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement path was unexpectedly removed: %v", err)
	}
	dir := filepath.Dir(path)
	_ = parsed.cleanup()
	_ = os.Remove(path)
	_ = os.Remove(replacement)
	_ = os.Remove(dir)
}

func TestCSVV3ImageSpoolRejectsSameInodeMutationAfterValidation(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	parsed, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	file := parsed.images[0].tempFile
	if file == nil {
		_ = parsed.cleanup()
		t.Fatal("image spool did not retain its descriptor")
	}
	originalSize := parsed.images[0].tempInfo.Size()
	if _, err := file.Seek(0, 0); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte{0x5a}, int(originalSize))); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if _, err := service.importCSVV3Parsed(context.Background(), &parsed, "replace"); err == nil || !strings.Contains(err.Error(), "内容") {
		_ = parsed.cleanup()
		t.Fatalf("same-inode image mutation result = %v", err)
	}
	if err := parsed.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestCSVV3TempImageReadReservationIsOwnedThroughConsumer(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	parsed, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.cleanup()
	size := parsed.images[0].tempInfo.Size()
	data, release, err := readCSVTempImage(parsed.images[0])
	if err != nil {
		t.Fatal(err)
	}
	if release == nil || len(data) != int(size) {
		if release != nil {
			release()
		}
		t.Fatalf("read reservation/data = %v/%d, want non-nil/%d", release != nil, len(data), size)
	}
	// Parsing retains one reservation for the private spool. Reading the
	// descriptor adds a second reservation for the heap buffer that the SQL
	// driver will consume. It must remain charged until the caller finishes the
	// INSERT, so a request that needs more than the remaining budget is rejected.
	if extra, ok := TryAcquireCSVTempBudget(MaxCSVTempBudgetBytes - 2*size + 1); ok {
		extra()
		release()
		t.Fatal("image read reservation was released before its consumer")
	}
	release()
	if extra, ok := TryAcquireCSVTempBudget(MaxCSVTempBudgetBytes - size); !ok {
		t.Fatal("image read reservation was not released after consumer completion")
	} else {
		extra()
	}
}

func TestCSVV3ImageSpoolCleanupDoesNotRemoveReplacementDirectory(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	parsed, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	dir := parsed.imageTempDir
	replacement := dir + ".renamed"
	if err := os.Rename(dir, replacement); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		_ = os.Rename(replacement, dir)
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	cleanupErr := parsed.cleanup()
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "identity") {
		_ = os.Remove(dir)
		_ = os.Remove(filepath.Join(replacement, "1.bin"))
		_ = os.Remove(replacement)
		t.Fatalf("replacement directory cleanup result = %v", cleanupErr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("replacement directory was removed: %v", err)
	}
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("original directory was unexpectedly removed: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(replacement); err != nil {
		t.Fatal(err)
	}
}

func TestCSVV3ImportRollsBackWhenImageSpoolCleanupFails(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-08-01", "item": "item", "type": "income", "amount": "1"},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1", "filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t))},
	)
	parsed, err := service.parseCSVV3Reader(context.Background(), strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	dir := parsed.imageTempDir
	replacement := dir + ".replacement"
	if err := os.Rename(dir, replacement); err != nil {
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		_ = os.Rename(replacement, dir)
		_ = parsed.cleanup()
		t.Fatal(err)
	}
	if _, err := service.importCSVV3Parsed(context.Background(), &parsed, "replace"); err == nil || !strings.Contains(err.Error(), "cleanup") {
		// Restore the original directory identity before the normal cleanup path.
		_ = os.Remove(dir)
		_ = os.Rename(replacement, dir)
		_ = parsed.cleanup()
		t.Fatalf("cleanup failure was not propagated: %v", err)
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("transaction count=%d after image cleanup failure; want rollback", count)
	}
	// The replacement pathname must not be removed as part of the failed
	// cleanup. Restore the retained directory identity, then let the same
	// identity-safe owner remove it without a recursive pathname operation.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, dir); err != nil {
		t.Fatal(err)
	}
	if err := parsed.cleanup(); err != nil {
		t.Fatalf("restored spool cleanup failed: %v", err)
	}
}

func TestCSVV3RoundTripPreservesExtendedLedgerDataAndRemapsIDs(t *testing.T) {
	instance, service := openCoreTestService(t, "csv-v3-source")
	_ = instance
	card, err := service.AddTransaction(models.TransactionRequest{Account: "card", Date: "2026-08-01", Item: "coffee", Type: "expense", Amount: 500, Memo: "card line 1\r\ncard line 2\\r"})
	if err != nil {
		t.Fatal(err)
	}
	bank, err := service.AddTransaction(models.TransactionRequest{Account: "bank", Date: "2026-08-02", Item: "card payment", Type: "expense", Amount: 500, Memo: "bank line 1\rbank line 2"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := service.CreateTag("work", nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTag("coffee", &root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddTransactionTags(card.ID, []int64{child.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveCreditCardSettings([]string{"card"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveBankAccountSettings([]string{"bank"}); err != nil {
		t.Fatal(err)
	}
	if err := service.AddTransactionLink(card.ID, bank.ID); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n" + "\x00\x00\x00\x00IEND\xaeB\x60\x82")
	// Use a valid 1x1 fixture generated by the shared image test helper.
	png = encodePNG(t)
	if _, err := service.AddTransactionImage(card.ID, models.TransactionImageRequest{
		Filename: "receipt.png", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(png),
	}); err != nil {
		t.Fatal(err)
	}

	content, err := service.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "record_type") || !strings.Contains(content, "\n3,transaction,") {
		t.Fatalf("expected CSV v3 export, got header/content: %q", content[:min(len(content), 240)])
	}
	if !strings.Contains(content, "data_base64") || !strings.Contains(content, "receipt.png") {
		t.Fatalf("v3 export did not include image columns: %q", content[:min(len(content), 400)])
	}

	// Import into an independent instance; IDs are allocated independently and
	// all associations must be rebuilt through the source-ID maps.
	target, targetService := openCoreTestService(t, "csv-v3-target")
	// Leave a row behind so SQLite's AUTOINCREMENT sequence differs from the
	// source. Replace must remap every relationship instead of passing only
	// because two empty databases happen to allocate identical IDs.
	oldResult, err := target.DB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES ('old-card', '2020-01-01', 'preexisting', 'income', 1, 1, 'old memo')`)
	if err != nil {
		t.Fatal(err)
	}
	oldTransactionID, err := oldResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	secondOld, err := targetService.AddTransaction(models.TransactionRequest{
		Account: "old-bank", Date: "2020-01-02", Item: "old-link", Type: "expense", Amount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldTag, err := targetService.CreateTag("obsolete", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := targetService.AddTransactionTags(oldTransactionID, []int64{oldTag.ID}); err != nil {
		t.Fatal(err)
	}
	if err := targetService.SaveCreditCardSettings([]string{"old-card"}); err != nil {
		t.Fatal(err)
	}
	if err := targetService.SaveBankAccountSettings([]string{"old-bank"}); err != nil {
		t.Fatal(err)
	}
	if err := targetService.AddTransactionLink(oldTransactionID, secondOld.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := targetService.AddTransactionImage(oldTransactionID, models.TransactionImageRequest{
		Filename: "obsolete.png", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(encodePNG(t)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := targetService.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "replace"); err != nil {
		t.Fatalf("ImportCSV v3: %v", err)
	}
	var transactionCount, imageCount, tagCount, tagLinkCount, linkCount int
	for query, destination := range map[string]*int{
		"SELECT COUNT(*) FROM transactions":       &transactionCount,
		"SELECT COUNT(*) FROM transaction_images": &imageCount,
		"SELECT COUNT(*) FROM tags":               &tagCount,
		"SELECT COUNT(*) FROM transaction_tags":   &tagLinkCount,
		"SELECT COUNT(*) FROM transaction_links":  &linkCount,
	} {
		if err := target.DB().QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if transactionCount != 2 || imageCount != 1 || tagCount != 2 || tagLinkCount != 1 || linkCount != 1 {
		t.Fatalf("counts transactions=%d images=%d tags=%d tag-links=%d links=%d", transactionCount, imageCount, tagCount, tagLinkCount, linkCount)
	}
	var sourceID, targetID int64
	if err := instance.DB().QueryRow("SELECT id FROM transactions WHERE item = 'coffee'").Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := target.DB().QueryRow("SELECT id FROM transactions WHERE item = 'coffee'").Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if targetID == sourceID {
		t.Fatalf("replace did not force an ID remap: source=%d target=%d", sourceID, targetID)
	}
	var sourceAccount, sourceDate, sourceItem, sourceType, sourceMemo string
	var sourceAmount, sourceBalance int64
	if err := instance.DB().QueryRow("SELECT account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", sourceID).Scan(&sourceAccount, &sourceDate, &sourceItem, &sourceType, &sourceAmount, &sourceBalance, &sourceMemo); err != nil {
		t.Fatal(err)
	}
	var targetAccount, targetDate, targetItem, targetType, targetMemo string
	var targetAmount, targetBalance int64
	if err := target.DB().QueryRow("SELECT account, date, item, type, amount, balance, memo FROM transactions WHERE id = ?", targetID).Scan(&targetAccount, &targetDate, &targetItem, &targetType, &targetAmount, &targetBalance, &targetMemo); err != nil {
		t.Fatal(err)
	}
	if sourceAccount != targetAccount || sourceDate != targetDate || sourceItem != targetItem || sourceType != targetType || sourceAmount != targetAmount || sourceBalance != targetBalance || sourceMemo != targetMemo {
		t.Fatalf("transaction fields changed: source=%q/%q/%q/%q/%d/%d/%q target=%q/%q/%q/%q/%d/%d/%q", sourceAccount, sourceDate, sourceItem, sourceType, sourceAmount, sourceBalance, sourceMemo, targetAccount, targetDate, targetItem, targetType, targetAmount, targetBalance, targetMemo)
	}
	var sourceFilename, sourceMIME, sourceCreated string
	var sourceData []byte
	if err := instance.DB().QueryRow("SELECT filename, mime_type, data, created_at FROM transaction_images WHERE transaction_id = ?", sourceID).Scan(&sourceFilename, &sourceMIME, &sourceData, &sourceCreated); err != nil {
		t.Fatal(err)
	}
	var targetFilename, targetMIME, targetCreated string
	var targetData []byte
	if err := target.DB().QueryRow("SELECT filename, mime_type, data, created_at FROM transaction_images WHERE transaction_id = ?", targetID).Scan(&targetFilename, &targetMIME, &targetData, &targetCreated); err != nil {
		t.Fatal(err)
	}
	if sourceFilename != targetFilename || sourceMIME != targetMIME || sourceCreated != targetCreated || !bytes.Equal(sourceData, targetData) {
		t.Fatalf("image fields changed: source=%q/%q/%q/%x target=%q/%q/%q/%x", sourceFilename, sourceMIME, sourceCreated, sourceData, targetFilename, targetMIME, targetCreated, targetData)
	}
	var sourceTagName, sourceParentName string
	if err := instance.DB().QueryRow(`SELECT child.name, COALESCE(parent.name, '') FROM tags child LEFT JOIN tags parent ON parent.id = child.parent_id WHERE child.id = ?`, child.ID).Scan(&sourceTagName, &sourceParentName); err != nil {
		t.Fatal(err)
	}
	var targetTagName, targetParentName string
	if err := target.DB().QueryRow(`SELECT child.name, COALESCE(parent.name, '') FROM tags child LEFT JOIN tags parent ON parent.id = child.parent_id WHERE child.name = ?`, sourceTagName).Scan(&targetTagName, &targetParentName); err != nil {
		t.Fatal(err)
	}
	if targetTagName != sourceTagName || targetParentName != sourceParentName {
		t.Fatalf("tag hierarchy changed: source=%q/%q target=%q/%q", sourceTagName, sourceParentName, targetTagName, targetParentName)
	}
	var linkedTargetID int64
	if err := target.DB().QueryRow(`SELECT CASE WHEN parent_id = ? THEN child_id ELSE parent_id END FROM transaction_links WHERE parent_id = ? OR child_id = ?`, targetID, targetID, targetID).Scan(&linkedTargetID); err != nil {
		t.Fatal(err)
	}
	var linkedItem string
	if err := target.DB().QueryRow("SELECT item FROM transactions WHERE id = ?", linkedTargetID).Scan(&linkedItem); err != nil {
		t.Fatal(err)
	}
	if linkedItem != "card payment" {
		t.Fatalf("transaction link target item = %q", linkedItem)
	}
	var linkedTagCount int
	if err := target.DB().QueryRow("SELECT COUNT(*) FROM transaction_tags tt JOIN tags t ON t.id = tt.tag_id WHERE tt.transaction_id = ? AND t.name = ?", targetID, sourceTagName).Scan(&linkedTagCount); err != nil {
		t.Fatal(err)
	}
	if linkedTagCount != 1 {
		t.Fatalf("transaction tag association count = %d", linkedTagCount)
	}
	var credit, bankSetting string
	if err := target.DB().QueryRow("SELECT value FROM settings WHERE key = 'credit_card_items'").Scan(&credit); err != nil {
		t.Fatal(err)
	}
	if err := target.DB().QueryRow("SELECT value FROM settings WHERE key = 'bank_account_items'").Scan(&bankSetting); err != nil {
		t.Fatal(err)
	}
	if credit != `["card"]` || bankSetting != `["bank"]` {
		t.Fatalf("settings = %q/%q", credit, bankSetting)
	}
	if card.ID == 1 && bank.ID == 2 {
		// The source and target normally happen to have the same IDs; force the
		// assertion to use the row's source IDs instead of relying on that fact.
		var targetID int64
		if err := target.DB().QueryRow("SELECT id FROM transactions WHERE item = 'coffee'").Scan(&targetID); err != nil {
			t.Fatal(err)
		}
		if targetID == card.ID {
			t.Log("SQLite allocated matching IDs in independent empty vaults; association checks above still verify remapping")
		}
	}
}

func TestCSVV3RejectsUnsafeVersionAndRollsBackReplace(t *testing.T) {
	setupCoreTestDB(t)
	originalID := insertTestTransaction(t, "keep", "2026-01-01", "original", "income", 100, 100)
	unsafe := strings.Join([]string{
		strings.Join(csvV3Headers, ","),
		"3,transaction,1,,0,0,0,=unsafe,2026-01-01,item,income,100,0,,,,,,,,,,,",
		"3,transaction,2,,0,0,0,cash,2026-01-02,item,expense,50,0,,,,,,,,,,,",
	}, "\n") + "\n"
	if _, err := ImportCSV(unsafe, "replace"); err == nil {
		t.Fatal("unsafe v3 formula cell was accepted")
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions WHERE id = ?", originalID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("original transaction count = %d, want 1 after rollback", count)
	}
}

func TestCSVV3ReplaceRequiresValidFinalManifestBeforeMutation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, records [][]string, headers map[string]int) [][]string
	}{
		{
			name: "header only",
			mutate: func(_ *testing.T, records [][]string, _ map[string]int) [][]string {
				return records[:1]
			},
		},
		{
			name: "row boundary truncation missing trailer",
			mutate: func(_ *testing.T, records [][]string, _ map[string]int) [][]string {
				return records[:len(records)-1]
			},
		},
		{
			name: "duplicate trailer",
			mutate: func(_ *testing.T, records [][]string, _ map[string]int) [][]string {
				return append(append([][]string(nil), records...), append([]string(nil), records[len(records)-1]...))
			},
		},
		{
			name: "tampered digest",
			mutate: func(t *testing.T, records [][]string, headers map[string]int) [][]string {
				copyRecords := cloneCSVRecords(records)
				manifestIndex := len(copyRecords) - 1
				value, err := decodeCSVV3TextCell(copyRecords[manifestIndex][headers["setting_value"]])
				if err != nil {
					t.Fatal(err)
				}
				var manifest csvV3Manifest
				if err := json.Unmarshal([]byte(value), &manifest); err != nil {
					t.Fatal(err)
				}
				manifest.Digest = strings.Repeat("0", sha256.Size*2)
				encoded, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				copyRecords[manifestIndex][headers["setting_value"]] = csvV3Text(string(encoded))
				return copyRecords
			},
		},
		{
			name: "tampered count",
			mutate: func(t *testing.T, records [][]string, headers map[string]int) [][]string {
				copyRecords := cloneCSVRecords(records)
				manifestIndex := len(copyRecords) - 1
				value, err := decodeCSVV3TextCell(copyRecords[manifestIndex][headers["setting_value"]])
				if err != nil {
					t.Fatal(err)
				}
				var manifest csvV3Manifest
				if err := json.Unmarshal([]byte(value), &manifest); err != nil {
					t.Fatal(err)
				}
				manifest.Counts["transaction"]++
				encoded, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				copyRecords[manifestIndex][headers["setting_value"]] = csvV3Text(string(encoded))
				return copyRecords
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"append", "replace"} {
				t.Run(mode, func(t *testing.T) {
					setupCoreTestDB(t)
					originalID := insertTestTransaction(t, "keep", "2026-01-01", "original", "income", 100, 100)
					valid := csvV3TestContent(t, map[string]string{
						csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash",
						"date": "2026-08-01", "item": "imported", "type": "income", "amount": "1",
					})
					records, headers := readCSVRecordsForTest(t, valid)
					invalid := writeCSVRecordsForTest(t, tc.mutate(t, records, headers))
					if _, err := ImportCSV(invalid, mode); err == nil {
						t.Fatal("invalid manifest was accepted")
					}
					var count int
					if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions WHERE id = ?", originalID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 1 {
						t.Fatalf("%s mutated the database after manifest failure: count=%d", mode, count)
					}
				})
			}
		})
	}
}

func cloneCSVRecords(records [][]string) [][]string {
	clone := make([][]string, len(records))
	for i := range records {
		clone[i] = append([]string(nil), records[i]...)
	}
	return clone
}

func readCSVRecordsForTest(t *testing.T, content string) ([][]string, map[string]int) {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	headers, err := csvV3HeaderMap(records[0])
	if err != nil {
		t.Fatal(err)
	}
	return records, headers
}

func writeCSVRecordsForTest(t *testing.T, records [][]string) string {
	t.Helper()
	var output strings.Builder
	writer := csv.NewWriter(&output)
	if err := writer.WriteAll(records); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestCSVV3RejectsDuplicateAndUnknownRowsBeforeWriting(t *testing.T) {
	setupCoreTestDB(t)
	base := strings.Join(csvV3Headers, ",") + "\n"
	row := "3,transaction,1,,0,0,0,cash,2026-01-01,item,income,100,0,,,,,,,,,,"
	duplicate := base + row + "\n" + row + "\n"
	if _, err := ImportCSV(duplicate, "replace"); err == nil || !strings.Contains(err.Error(), "重複") {
		t.Fatalf("duplicate v3 transaction result = %v", err)
	}
	unknownFields := append([]string{"3", "unknown"}, make([]string, len(csvV3Headers)-2)...)
	unknown := base + strings.Join(unknownFields, ",") + "\n"
	if _, err := ImportCSV(unknown, "replace"); err == nil || !strings.Contains(err.Error(), "record_type") {
		t.Fatalf("unknown v3 record result = %v", err)
	}
}

func TestCSVV3ReplacePreservesOtherSettingsAndRenormalizesRemappedLinks(t *testing.T) {
	setupCoreTestDB(t)
	insertTestTransaction(t, "keep", "2026-01-01", "before-replace", "income", 1, 1)
	if _, err := database.GetDB().Exec("INSERT INTO settings (key, value) VALUES ('future_setting', 'preserve-me')"); err != nil {
		t.Fatal(err)
	}
	content := csvV3TestContent(t,
		// Deliberately reverse source row order. The source link is normalized
		// by source IDs, while insertion allocates target IDs in row order.
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "2", "account": "bank", "date": "2026-01-02", "item": "bank-side", "type": "expense", "amount": "100"},
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "card", "date": "2026-01-01", "item": "card-side", "type": "expense", "amount": "100"},
		map[string]string{csvVersionHeader: "3", "record_type": "setting", "setting_key": "credit_card_items", "setting_value": `[" card "]`},
		map[string]string{csvVersionHeader: "3", "record_type": "setting", "setting_key": "bank_account_items", "setting_value": `[" bank "]`},
		map[string]string{csvVersionHeader: "3", "record_type": "transaction_link", "parent_id": "1", "child_id": "2"},
	)
	if count, err := ImportCSV(content, "replace"); err != nil || count != 2 {
		t.Fatalf("replace count = %d, err = %v", count, err)
	}
	var parentID, childID int64
	if err := database.GetDB().QueryRow("SELECT parent_id, child_id FROM transaction_links").Scan(&parentID, &childID); err != nil {
		t.Fatal(err)
	}
	if parentID >= childID {
		t.Fatalf("link endpoints = %d,%d, want canonical ascending order", parentID, childID)
	}
	var parentAccount, childAccount string
	if err := database.GetDB().QueryRow("SELECT account FROM transactions WHERE id = ?", parentID).Scan(&parentAccount); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow("SELECT account FROM transactions WHERE id = ?", childID).Scan(&childAccount); err != nil {
		t.Fatal(err)
	}
	if parentAccount != "bank" || childAccount != "card" {
		t.Fatalf("remapped link accounts = %q,%q, want bank,card", parentAccount, childAccount)
	}
	var preserved string
	if err := database.GetDB().QueryRow("SELECT value FROM settings WHERE key = 'future_setting'").Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != "preserve-me" {
		t.Fatalf("future setting = %q, want preserve-me", preserved)
	}
	var credit, bankSetting string
	if err := database.GetDB().QueryRow("SELECT value FROM settings WHERE key = 'credit_card_items'").Scan(&credit); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow("SELECT value FROM settings WHERE key = 'bank_account_items'").Scan(&bankSetting); err != nil {
		t.Fatal(err)
	}
	if credit != `[" card "]` || bankSetting != `[" bank "]` {
		t.Fatalf("whitespace settings changed: %q/%q", credit, bankSetting)
	}
}

func TestCSVV3AppendPreservesExistingLinksAndRejectsSettingConflictsAtomically(t *testing.T) {
	instance, service := openCoreTestService(t, "csv-v3-append")
	card, err := service.AddTransaction(models.TransactionRequest{Account: "card", Date: "2026-08-01", Item: "card-side", Type: "expense", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	bank, err := service.AddTransaction(models.TransactionRequest{Account: "bank", Date: "2026-08-02", Item: "bank-side", Type: "expense", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SaveCreditCardSettings([]string{"card"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveBankAccountSettings([]string{"bank"}); err != nil {
		t.Fatal(err)
	}
	if err := service.AddTransactionLink(card.ID, bank.ID); err != nil {
		t.Fatal(err)
	}
	appendOnly := csvV3TestContent(t, map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "9",
		"account": "card", "date": "2026-08-03", "item": "new", "type": "expense", "amount": "10",
	})
	if count, err := service.ImportCSVReaderContext(context.Background(), strings.NewReader(appendOnly), "append"); err != nil || count != 1 {
		t.Fatalf("append result count=%d err=%v", count, err)
	}
	var links int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("append pruned existing links: count=%d", links)
	}

	conflict := csvV3TestContent(t, map[string]string{
		csvVersionHeader: "3", "record_type": "setting", "setting_key": "credit_card_items", "setting_value": `["other"]`,
	})
	if _, err := service.ImportCSVReaderContext(context.Background(), strings.NewReader(conflict), "append"); err == nil || !strings.Contains(err.Error(), "上書き") {
		t.Fatalf("append setting conflict result = %v", err)
	}
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("failed append removed existing links: count=%d", links)
	}
	var setting string
	if err := instance.DB().QueryRow("SELECT value FROM settings WHERE key = 'credit_card_items'").Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != `["card"]` {
		t.Fatalf("failed append changed existing setting: %q", setting)
	}
	if _, err := instance.DB().Exec("UPDATE settings SET value = '{not-json}' WHERE key = 'credit_card_items'"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportCSVReaderContext(context.Background(), strings.NewReader(appendOnly), "append"); err == nil || !strings.Contains(err.Error(), "既存のledger設定") {
		t.Fatalf("malformed existing setting append result = %v", err)
	}
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("malformed setting append removed existing links: count=%d", links)
	}
}

func TestCSVLinkPolicyCanonicalizesHistoricalSettingWhitespace(t *testing.T) {
	instance, service := openCoreTestService(t, "csv-whitespace-settings")
	card, err := service.AddTransaction(models.TransactionRequest{Account: "card", Date: "2026-08-01", Item: "card", Type: "expense", Amount: 10})
	if err != nil {
		t.Fatal(err)
	}
	bank, err := service.AddTransaction(models.TransactionRequest{Account: "bank", Date: "2026-08-02", Item: "bank", Type: "expense", Amount: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SaveCreditCardSettings([]string{" card "}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveBankAccountSettings([]string{" bank "}); err != nil {
		t.Fatal(err)
	}
	if err := service.AddTransactionLink(card.ID, bank.ID); err != nil {
		t.Fatalf("historical whitespace settings rejected link: %v", err)
	}
	var links int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("link count=%d, want 1", links)
	}
	if err := service.SaveCreditCardSettings([]string{" card "}); err != nil {
		t.Fatal(err)
	}
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("link was pruned after equivalent setting save: %d", links)
	}
}

func TestCSVV3LegacyTagRowPreservesPreValidatorName(t *testing.T) {
	setupCoreTestDB(t)
	if _, err := database.GetDB().Exec("INSERT INTO tags (name, parent_id, level) VALUES (' old/tag ', NULL, 1)"); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "tag_legacy") {
		t.Fatalf("legacy tag was not marked explicitly: %q", content)
	}
	if _, err := ImportCSV(content, "replace"); err != nil {
		t.Fatalf("legacy tag restore: %v", err)
	}
	var name string
	if err := database.GetDB().QueryRow("SELECT name FROM tags").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != " old/tag " {
		t.Fatalf("legacy tag name = %q", name)
	}
}

func TestCSVV3ArchivesAndRestoresDuplicateRootTagsWithoutMerging(t *testing.T) {
	setupCoreTestDB(t)
	if _, err := database.GetDB().Exec(`INSERT INTO tags (name, parent_id, level, legacy_duplicate) VALUES ('same', NULL, 1, 0), ('same', NULL, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "tag_legacy") {
		t.Fatalf("duplicate root was not archived explicitly: %q", content)
	}
	if _, err := ImportCSV(content, "replace"); err != nil {
		t.Fatal(err)
	}
	var count, archived int
	if err := database.GetDB().QueryRow("SELECT COUNT(*), COALESCE(SUM(legacy_duplicate), 0) FROM tags WHERE name = 'same' AND parent_id IS NULL").Scan(&count, &archived); err != nil {
		t.Fatal(err)
	}
	if count != 2 || archived != 1 {
		t.Fatalf("duplicate roots were merged or lost: count=%d archived=%d", count, archived)
	}
}

func TestCSVV3ArchivesLegacySettingsAcceptedByPreviousAPI(t *testing.T) {
	setupCoreTestDB(t)
	legacy := `[` + `"` + strings.Repeat("x", 256) + `","", ""` + `,"x"]`
	cardID := insertTestTransaction(t, "x", "2026-08-01", "card-side", "expense", 10, -10)
	bankID := insertTestTransaction(t, "bank", "2026-08-02", "bank-side", "expense", 10, -10)
	if _, err := database.GetDB().Exec("INSERT INTO settings (key, value) VALUES ('credit_card_items', ?), ('bank_account_items', '[\"bank\"]')", legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec("INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)", cardID, bankID); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "setting_legacy") {
		t.Fatalf("legacy setting was not archived explicitly: %q", content)
	}
	if _, err := ImportCSV(content, "replace"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := database.GetDB().QueryRow("SELECT value FROM settings WHERE key = 'credit_card_items'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("legacy setting changed: got %q want %q", got, legacy)
	}
	var links int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transaction_links").Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("legacy settings restore pruned existing link: %d", links)
	}
}

func csvV3TestContent(t *testing.T, rows ...map[string]string) string {
	t.Helper()
	var output strings.Builder
	writer := csv.NewWriter(&output)
	if err := writer.Write(csvV3Headers); err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	counts := make(map[string]int64)
	for _, values := range rows {
		record := csvV3Record(values)
		if err := writer.Write(record); err != nil {
			t.Fatal(err)
		}
		updateCSVV3Digest(digest, record)
		counts[values["record_type"]]++
	}
	manifestValue, err := newCSVV3Manifest(counts, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(csvV3Record(map[string]string{
		csvVersionHeader: csvVersion3, "record_type": csvV3ManifestRecordType,
		"setting_key": csvV3ManifestKey, "setting_value": manifestValue,
	})); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestCSVV3RejectsUnsafeTagSettingsAndCreatedAt(t *testing.T) {
	transaction := map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "1",
		"account": "cash", "date": "2026-01-01", "item": "item", "type": "income", "amount": "100",
	}
	tests := []struct {
		name string
		row  map[string]string
		want string
	}{
		{
			name: "tag slash",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "tag", "id": "1", "tag_name": "bad/name", "tag_level": "1"},
			want: "タグ名",
		},
		{
			name: "tag non canonical whitespace",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "tag", "id": "1", "tag_name": encodeCSVTextCell(" root "), "tag_level": "1"},
			want: "正規化済み",
		},
		{
			name: "tag parent level",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "tag", "id": "1", "tag_name": "child", "tag_parent_id": "99", "tag_level": "2"},
			want: "タグ親が見つかりません",
		},
		{
			name: "unknown setting",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "setting", "setting_key": "future_secret", "setting_value": "[]"},
			want: "ledger設定キー",
		},
		{
			name: "null setting",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "setting", "setting_key": "credit_card_items", "setting_value": "null"},
			want: "文字列配列JSON",
		},
		{
			name: "duplicate setting item",
			row:  map[string]string{csvVersionHeader: "3", "record_type": "setting", "setting_key": "credit_card_items", "setting_value": `["card","card"]`},
			want: "重複",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := csvV3TestContent(t, transaction, tt.row)
			if _, err := (&Service{}).parseCSVV3(content); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parse result = %v, want %q", err, tt.want)
			}
		})
	}

	tooLongName := strings.Repeat("a", validation.MaxTagNameBytes+1)
	if _, err := (&Service{}).parseCSVV3(csvV3TestContent(t, map[string]string{
		csvVersionHeader: "3", "record_type": "tag", "id": "1", "tag_name": tooLongName, "tag_level": "1",
	})); err == nil || !strings.Contains(err.Error(), "255バイト") {
		t.Fatalf("long tag result = %v", err)
	}

	root := map[string]string{csvVersionHeader: "3", "record_type": "tag", "id": "1", "tag_name": "root", "tag_level": "1"}
	badChild := map[string]string{csvVersionHeader: "3", "record_type": "tag", "id": "2", "tag_name": "child", "tag_parent_id": "1", "tag_level": "3"}
	if _, err := (&Service{}).parseCSVV3(csvV3TestContent(t, root, badChild)); err == nil || !strings.Contains(err.Error(), "親の階層") {
		t.Fatalf("parent level result = %v", err)
	}

	badImage := map[string]string{
		csvVersionHeader: "3", "record_type": "image", "id": "1", "transaction_id": "1",
		"filename": "receipt.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(encodePNG(t)),
		"created_at": "not-a-date",
	}
	if _, err := (&Service{}).parseCSVV3(csvV3TestContent(t, transaction, badImage)); err == nil || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("created_at result = %v", err)
	}
}

func TestCSVV3RequiresExactRecordFieldCount(t *testing.T) {
	content := strings.Join(csvV3Headers, ",") + "\n3,transaction,1\n"
	if _, err := (&Service{}).parseCSVV3(content); err == nil || !strings.Contains(err.Error(), "列数") {
		t.Fatalf("short row result = %v", err)
	}
}

func TestCSVV3AppliesTransactionTextLimits(t *testing.T) {
	base := map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "1",
		"account": "cash", "date": "2026-01-01", "item": "item", "type": "income", "amount": "100",
	}
	for _, tt := range []struct {
		name, field, want string
		limit             int
	}{
		{name: "account", field: "account", want: "口座名", limit: validation.MaxAccountBytes},
		{name: "item", field: "item", want: "項目", limit: validation.MaxItemBytes},
		{name: "memo", field: "memo", want: "メモ", limit: validation.MaxMemoBytes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := make(map[string]string, len(base)+1)
			for key, value := range base {
				row[key] = value
			}
			row[tt.field] = strings.Repeat("x", tt.limit+1)
			if _, err := (&Service{}).parseCSVV3(csvV3TestContent(t, row)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("%s result = %v", tt.name, err)
			}
		})
	}
}

func TestCSVV3ParsedTextQuotaIsStrict(t *testing.T) {
	parsed := csvV3Import{parsedTextBytes: maxCSVParsedTextBytes}
	if err := parsed.addParsedText("x"); err == nil {
		t.Fatal("parsed text quota accepted data over the limit")
	}
	parsed = csvV3Import{parsedTextBytes: maxCSVParsedTextBytes - 1}
	if err := parsed.addParsedText("x"); err != nil {
		t.Fatalf("last byte of parsed text quota rejected: %v", err)
	}
}

func TestCSVV3ReaderHonorsCancellationBeforeMutatingDatabase(t *testing.T) {
	setupCoreTestDB(t)
	originalID := insertTestTransaction(t, "keep", "2026-01-01", "original", "income", 100, 100)
	content := csvV3TestContent(t, map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "1",
		"account": "cash", "date": "2026-01-01", "item": "new", "type": "income", "amount": "100",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Service{}).ImportCSVReaderContext(ctx, strings.NewReader(content), "replace"); err == nil {
		t.Fatal("canceled CSV reader unexpectedly succeeded")
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions WHERE id = ?", originalID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("original transaction count after cancellation = %d, want 1", count)
	}
}

func TestCSVReaderBoundsHeaderProbe(t *testing.T) {
	input := bufio.NewReader(strings.NewReader(strings.Repeat("x", maxCSVHeaderBytes+1)))
	if _, err := readCSVHeaderLine(input); err == nil {
		t.Fatal("oversized CSV header was accepted")
	}
}

func TestCSVImportReleasesCoreSlotAfterValidationError(t *testing.T) {
	setupCoreTestDB(t)
	invalid := csvV3TestContent(t, map[string]string{
		csvVersionHeader: "3", "record_type": "transaction", "id": "1",
		"account": "cash", "date": "not-a-date", "item": "item", "type": "income", "amount": "100",
	})
	if _, err := ImportCSV(invalid, "replace"); err == nil {
		t.Fatal("invalid CSV unexpectedly imported")
	}
	valid := "account,date,item,type,amount\ncash,2026-01-01,item,income,100\n"
	if count, err := ImportCSV(valid, "append"); err != nil || count != 1 {
		t.Fatalf("valid import after error = count %d, err %v", count, err)
	}
}

func TestCSVV3ReplaceRollsBackAfterDatabaseFailure(t *testing.T) {
	setupCoreTestDB(t)
	originalID := insertTestTransaction(t, "keep", "2026-01-01", "original", "income", 100, 100)
	if _, err := database.GetDB().Exec(`CREATE TRIGGER csv_v3_test_abort
		BEFORE INSERT ON transactions WHEN NEW.account = 'abort'
		BEGIN SELECT RAISE(ABORT, 'test import failure'); END`); err != nil {
		t.Fatal(err)
	}
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-01-01", "item": "new", "type": "income", "amount": "100"},
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "2", "account": "abort", "date": "2026-01-02", "item": "must rollback", "type": "expense", "amount": "50"},
	)
	if _, err := ImportCSV(content, "replace"); err == nil {
		t.Fatal("database failure was not returned")
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("transaction count after rollback = %d, want 1", count)
	}
	var item string
	if err := database.GetDB().QueryRow("SELECT item FROM transactions WHERE id = ?", originalID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if item != "original" {
		t.Fatalf("original transaction item = %q", item)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
