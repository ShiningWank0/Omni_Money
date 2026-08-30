package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestLegacyCSVLargeInt64AmountUsesArchiveProvenanceAndRoundTrips(t *testing.T) {
	const legacyAmount int64 = 1_000_000_001
	for _, version := range []string{"unversioned", "v1", "v2"} {
		for _, path := range []string{"string", "raw stream"} {
			t.Run(version+"/"+path, func(t *testing.T) {
				setupCoreTestDB(t)
				service := &Service{db: database.GetDB(), legacy: true}
				content := "account,date,item,type,amount,memo\nlegacy,2026-01-01,old,income,1000000001,kept\n"
				if version == "v1" || version == "v2" {
					value := map[string]string{"v1": "1", "v2": "2"}[version]
					content = "account,date,item,type,amount,memo,omni_money_csv_version\nlegacy,2026-01-01,old,income,1000000001,kept," + value + "\n"
				}
				var imported int
				var err error
				if path == "string" {
					imported, err = service.ImportCSV(content, "append")
				} else {
					imported, err = service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append")
				}
				if err != nil || imported != 1 {
					t.Fatalf("legacy amount import = %d, %v", imported, err)
				}
				var stored, archived, balance int64
				if err := database.GetDB().QueryRow(`SELECT t.amount, a.amount, t.balance FROM transactions t
				JOIN transaction_archive_amounts a ON a.transaction_id = t.id`).Scan(&stored, &archived, &balance); err != nil {
					t.Fatal(err)
				}
				if stored != 1_000_000_000 || archived != legacyAmount || balance != legacyAmount {
					t.Fatalf("archive amount storage = %d/%d balance=%d", stored, archived, balance)
				}
				archive, err := service.BackupToCSV()
				if err != nil || !strings.Contains(archive, "transaction_legacy") || !strings.Contains(archive, "1000000001") {
					t.Fatalf("v3 archive did not preserve amount: %v %q", err, archive)
				}
				if _, err := service.ImportCSV(archive, "replace"); err != nil {
					t.Fatalf("v3 restore: %v", err)
				}
				var effective int64
				if err := database.GetDB().QueryRow(`SELECT COALESCE(a.amount, t.amount) FROM transactions t
				LEFT JOIN transaction_archive_amounts a ON a.transaction_id = t.id`).Scan(&effective); err != nil {
					t.Fatal(err)
				}
				if effective != legacyAmount {
					t.Fatalf("restored amount = %d", effective)
				}
			})
		}
	}
}

func TestCSVV3LegacyImagePreservesMetadataAndOpaqueBlob(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	result, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'old', 'expense', 1, -1, '')`)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := result.LastInsertId()
	wantData := []byte("not-an-image")
	wantFilename, wantMIME := " Receipt.PNG ", " IMAGE/PNG "
	if _, err := database.GetDB().Exec(`INSERT INTO transaction_images (transaction_id, filename, data, mime_type)
		VALUES (?, ?, ?, ?)`, txID, wantFilename, wantData, wantMIME); err != nil {
		t.Fatal(err)
	}
	archive, err := service.BackupToCSV()
	if err != nil || !strings.Contains(archive, "image_legacy") {
		t.Fatalf("legacy image export: %v %q", err, archive)
	}
	if _, err := service.ImportCSV(archive, "replace"); err != nil {
		t.Fatalf("legacy image restore: %v", err)
	}
	var filename, mime string
	var data []byte
	if err := database.GetDB().QueryRow(`SELECT filename, mime_type, data FROM transaction_image_archive`).Scan(&filename, &mime, &data); err != nil {
		t.Fatal(err)
	}
	if filename != wantFilename || mime != wantMIME || !bytes.Equal(data, wantData) {
		t.Fatalf("legacy image changed: %q/%q/%x", filename, mime, data)
	}
	var restoredTxID int64
	if err := database.GetDB().QueryRow(`SELECT transaction_id FROM transaction_image_archive`).Scan(&restoredTxID); err != nil {
		t.Fatal(err)
	}
	images, err := service.GetTransactionImages(restoredTxID)
	if err != nil || len(images) != 1 || !images[0].Invalid || images[0].DataURL != "" {
		t.Fatalf("unsafe legacy image was exposed: %#v %v", images, err)
	}
}

func TestCSVV3RestoresPreQuotaTransactionImageSet(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	result, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'old', 'expense', 1, -1, '')`)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := result.LastInsertId()
	if _, err := database.GetDB().Exec(`DROP TRIGGER trg_transaction_images_quota_insert`); err != nil {
		t.Fatal(err)
	}
	png := encodePNG(t)
	for i := 0; i < 11; i++ {
		if _, err := database.GetDB().Exec(`INSERT INTO transaction_images (transaction_id, filename, data, mime_type)
			VALUES (?, ?, ?, 'image/png')`, txID, fmt.Sprintf("old-%02d.png", i), png); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := service.BackupToCSV()
	if err != nil || strings.Count(archive, "image_legacy") < 11 {
		t.Fatalf("pre-quota export: %v legacy rows=%d", err, strings.Count(archive, "image_legacy"))
	}
	if _, err := service.ImportCSV(archive, "replace"); err != nil {
		t.Fatalf("pre-quota restore: %v", err)
	}
	var restored int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM transaction_image_archive`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 11 {
		t.Fatalf("restored archive images = %d", restored)
	}
}

func TestCSVV3MixedImageRowsRestoreIndependentOfRowOrder(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	png := encodePNG(t)
	content := csvV3TestContent(t,
		map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-01-01", "item": "old", "type": "expense", "amount": "1"},
		// Deliberately place archive-only data before the current image. Import
		// must normalize insertion order before DB triggers observe the rows.
		map[string]string{csvVersionHeader: "3", "record_type": "image_legacy", "id": "-1", "transaction_id": "1", "filename": "", "mime_type": "", "data_base64": ""},
		map[string]string{csvVersionHeader: "3", "record_type": "image", "id": "2", "transaction_id": "1", "filename": "current.png", "mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(png)},
	)
	if _, err := service.ImportCSV(content, "replace"); err != nil {
		t.Fatalf("mixed row restore: %v", err)
	}
	for table, want := range map[string]int{"transaction_images": 1, "transaction_image_archive": 1} {
		var got int
		if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count = %d/%v, want %d", table, got, err, want)
		}
	}
}

func TestCSVV3PreservesBoundedEmptyAndLargeLegacyImages(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	result, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'old', 'expense', 1, -1, '')`)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := result.LastInsertId()
	large := bytes.Repeat([]byte{0x5a}, 6*1024*1024)
	for _, image := range []struct {
		filename string
		mime     string
		data     []byte
	}{{"", "", []byte{}}, {" legacy.bin ", " APPLICATION/OCTET-STREAM ", large}} {
		if _, err := database.GetDB().Exec(`INSERT INTO transaction_image_archive (transaction_id, filename, data, mime_type)
			VALUES (?, ?, ?, ?)`, txID, image.filename, image.data, image.mime); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := service.BackupToCSV()
	if err != nil {
		t.Fatalf("export bounded legacy images: %v", err)
	}
	if _, err := service.ImportCSV(archive, "replace"); err != nil {
		t.Fatalf("restore bounded legacy images: %v", err)
	}
	rows, err := database.GetDB().Query(`SELECT filename, mime_type, data FROM transaction_image_archive ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var filename, mime string
		var data []byte
		if err := rows.Scan(&filename, &mime, &data); err != nil {
			t.Fatal(err)
		}
		switch filename {
		case "":
			if mime != "" || len(data) != 0 {
				t.Fatalf("empty legacy image changed: %q/%d", mime, len(data))
			}
			seen["empty"] = true
		case " legacy.bin ":
			if mime != " APPLICATION/OCTET-STREAM " || !bytes.Equal(data, large) {
				t.Fatalf("large legacy image changed: %q/%d", mime, len(data))
			}
			seen["large"] = true
		default:
			t.Fatalf("restored unexpected legacy filename %q", filename)
		}
	}
	if !seen["empty"] || !seen["large"] {
		t.Fatalf("restored legacy images = %#v", seen)
	}
}

func TestTransactionImagePaginationAndGrandfatheredAccountMove(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	result, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('old-account', '2026-01-01', 'old', 'expense', 1, -1, '')`)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := result.LastInsertId()
	for i := 0; i < 11; i++ {
		if _, err := database.GetDB().Exec(`INSERT INTO transaction_image_archive (transaction_id, filename, data, mime_type)
			VALUES (?, ?, X'', '')`, txID, fmt.Sprintf("legacy-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.GetTransactionImagesPage(txID, "", 2)
	if err != nil || len(first.Images) != 2 || first.NextCursor != "2" {
		t.Fatalf("first image page = %#v/%v", first, err)
	}
	second, err := service.GetTransactionImagesPage(txID, "10", 2)
	if err != nil || len(second.Images) != 1 || second.NextCursor != "" {
		t.Fatalf("second image page = %#v/%v", second, err)
	}
	if _, err := service.GetTransactionImages(txID); err == nil || !strings.Contains(err.Error(), "pagination") {
		t.Fatalf("unbounded compatibility list result = %v", err)
	}
	updated, err := service.UpdateTransaction(txID, models.TransactionRequest{
		Account: "new-account", Date: "2026-01-01", Item: "moved", Type: "expense", Amount: 1,
	})
	if err != nil {
		t.Fatalf("zero-image grandfathered account move: %v", err)
	}
	if updated.Account != "new-account" {
		t.Fatalf("updated account = %q", updated.Account)
	}
}

func TestCSVV3RejectsArchiveImageCountAndMetadataBeyondBounds(t *testing.T) {
	t.Run("per transaction count", func(t *testing.T) {
		setupCoreTestDB(t)
		rows := make([]map[string]string, 0, models.MaxArchivedImagesPerTransaction+2)
		rows = append(rows, map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-01-01", "item": "old", "type": "income", "amount": "1"})
		for i := 1; i <= models.MaxArchivedImagesPerTransaction+1; i++ {
			rows = append(rows, map[string]string{csvVersionHeader: "3", "record_type": "image_legacy", "id": fmt.Sprintf("-%d", i), "transaction_id": "1", "filename": "", "mime_type": "", "data_base64": ""})
		}
		content := csvV3TestContent(t, rows...)
		service := &Service{db: database.GetDB(), legacy: true}
		if _, err := service.ImportCSV(content, "replace"); err == nil || !strings.Contains(err.Error(), "archive上限") {
			t.Fatalf("archive count result = %v", err)
		}
	})

	t.Run("metadata bytes", func(t *testing.T) {
		setupCoreTestDB(t)
		content := csvV3TestContent(t,
			map[string]string{csvVersionHeader: "3", "record_type": "transaction", "id": "1", "account": "cash", "date": "2026-01-01", "item": "old", "type": "income", "amount": "1"},
			map[string]string{csvVersionHeader: "3", "record_type": "image_legacy", "id": "-1", "transaction_id": "1", "filename": strings.Repeat("x", models.MaxArchivedImageMetadataBytes+1), "mime_type": "", "data_base64": ""},
		)
		service := &Service{db: database.GetDB(), legacy: true}
		if _, err := service.ImportCSV(content, "replace"); err == nil || !strings.Contains(err.Error(), "4096") {
			t.Fatalf("archive metadata result = %v", err)
		}
	})
}

func TestLegacyCSVAppendNeverPrunesExistingExtensions(t *testing.T) {
	for _, version := range []string{"unversioned", "v1", "v2"} {
		for _, path := range []string{"string", "raw stream"} {
			t.Run(version+"/"+path, func(t *testing.T) {
				setupCoreTestDB(t)
				service := &Service{db: database.GetDB(), legacy: true}
				first, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
					VALUES ('card', '2026-01-01', 'a', 'expense', 1, -1, '')`)
				if err != nil {
					t.Fatal(err)
				}
				second, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
					VALUES ('bank', '2026-01-02', 'b', 'income', 1, 1, '')`)
				if err != nil {
					t.Fatal(err)
				}
				firstID, _ := first.LastInsertId()
				secondID, _ := second.LastInsertId()
				tag, err := database.GetDB().Exec(`INSERT INTO tags (name, level) VALUES ('kept', 1)`)
				if err != nil {
					t.Fatal(err)
				}
				tagID, _ := tag.LastInsertId()
				png := encodePNG(t)
				statements := []struct {
					query string
					args  []any
				}{
					{`INSERT INTO transaction_images (transaction_id, filename, data, mime_type) VALUES (?, 'kept.png', ?, 'image/png')`, []any{firstID, png}},
					{`INSERT INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)`, []any{firstID, tagID}},
					{`INSERT INTO transaction_links (parent_id, child_id) VALUES (?, ?)`, []any{firstID, secondID}},
					{`INSERT OR REPLACE INTO settings (key, value) VALUES ('credit_card_items', '["card"]')`, nil},
					{`INSERT OR REPLACE INTO settings (key, value) VALUES ('bank_account_items', '["bank"]')`, nil},
				}
				for _, statement := range statements {
					if _, err := database.GetDB().Exec(statement.query, statement.args...); err != nil {
						t.Fatal(err)
					}
				}
				content := "account,date,item,type,amount,memo\nwallet,2026-02-01,new,income,2,memo\n"
				if version == "v1" || version == "v2" {
					value := map[string]string{"v1": "1", "v2": "2"}[version]
					content = "account,date,item,type,amount,memo,omni_money_csv_version\nwallet,2026-02-01,new,income,2,memo," + value + "\n"
				}
				var imported int
				if path == "string" {
					imported, err = service.ImportCSV(content, "append")
				} else {
					imported, err = service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append")
				}
				if err != nil || imported != 1 {
					t.Fatalf("legacy append = %d, %v", imported, err)
				}
				for table, want := range map[string]int{"transaction_images": 1, "tags": 1, "transaction_tags": 1, "transaction_links": 1, "settings": 2} {
					var got int
					if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
						t.Fatalf("%s after append = %d/%v, want %d", table, got, err, want)
					}
				}
			})
		}
	}
}

func TestCSVV3AcceptsManifestFromBeforeImageLegacyExtension(t *testing.T) {
	setupCoreTestDB(t)
	service := &Service{db: database.GetDB(), legacy: true}
	if _, err := database.GetDB().Exec(`INSERT INTO transactions (account, date, item, type, amount, balance, memo)
		VALUES ('cash', '2026-01-01', 'kept', 'income', 1, 1, '')`); err != nil {
		t.Fatal(err)
	}
	archive, err := service.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(archive)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]int{}
	for i, name := range records[0] {
		header[name] = i
	}
	manifestRow := records[len(records)-1]
	value, err := decodeCSVV3TextCell(manifestRow[header["setting_value"]])
	if err != nil {
		t.Fatal(err)
	}
	var manifest csvV3Manifest
	if err := json.Unmarshal([]byte(value), &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest.Counts, "image_legacy")
	legacyJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestRow[header["setting_value"]] = csvV3Text(string(legacyJSON))
	var rewritten strings.Builder
	writer := csv.NewWriter(&rewritten)
	writer.WriteAll(records)
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	if imported, err := service.ImportCSV(rewritten.String(), "replace"); err != nil || imported != 1 {
		t.Fatalf("old v3 manifest import = %d, %v", imported, err)
	}
}

func TestImportCSVV1PreservesHistoricalTextBeyondNewWriteLimits(t *testing.T) {
	wantAccount := strings.Repeat("a", 300)
	wantItem := strings.Repeat("項", 300)
	wantMemo := "legacy 👨‍👩‍👧‍👦 memo"
	content := "account,date,item,type,amount,memo\n" +
		wantAccount + ",2026-01-01," + wantItem + ",expense,123," + wantMemo + "\n"
	for _, name := range []string{"string", "raw stream"} {
		t.Run(name, func(t *testing.T) {
			setupCoreTestDB(t)
			var imported int
			var err error
			if name == "string" {
				imported, err = ImportCSV(content, "append")
			} else {
				service := &Service{db: database.GetDB(), legacy: true}
				imported, err = service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append")
			}
			if err != nil || imported != 1 {
				t.Fatalf("v1 historical import = %d, %v", imported, err)
			}
			var account, item, memo string
			if err := database.GetDB().QueryRow("SELECT account, item, memo FROM transactions").Scan(&account, &item, &memo); err != nil {
				t.Fatal(err)
			}
			if account != wantAccount || item != wantItem || memo != wantMemo {
				t.Fatalf("v1 historical text changed: account=%d/%d item=%d/%d memo=%q/%q", len([]byte(account)), len([]byte(wantAccount)), len([]byte(item)), len([]byte(wantItem)), memo, wantMemo)
			}
		})
	}
}

func TestImportCSVV1AllowsHistoricalFormulaLeadingTextAndSafeZWJ(t *testing.T) {
	cases := []struct {
		name                string
		account, item, memo string
	}{
		{name: "plus", account: "+Savings", item: "plain", memo: "memo"},
		{name: "minus", account: "cash", item: "-cash", memo: "memo"},
		{name: "at", account: "cash", item: "plain", memo: "@home"},
		{name: "equals and zwj", account: "=name", item: "plain", memo: "家族👨‍👩‍👧‍👦"},
	}
	for _, tc := range cases {
		for _, path := range []string{"string", "raw stream"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				setupCoreTestDB(t)
				content := "account,date,item,type,amount,memo\n" + tc.account + ",2026-01-01," + tc.item + ",income,1," + tc.memo + "\n"
				var imported int
				var err error
				if path == "string" {
					imported, err = ImportCSV(content, "append")
				} else {
					service := &Service{db: database.GetDB(), legacy: true}
					imported, err = service.ImportCSVReaderContext(context.Background(), strings.NewReader(content), "append")
				}
				if err != nil || imported != 1 {
					t.Fatalf("historical v1 import = %d, %v", imported, err)
				}
				var account, item, memo string
				if err := database.GetDB().QueryRow("SELECT account, item, memo FROM transactions").Scan(&account, &item, &memo); err != nil {
					t.Fatal(err)
				}
				if account != tc.account || item != tc.item || memo != tc.memo {
					t.Fatalf("historical v1 text changed: got %q/%q/%q", account, item, memo)
				}
			})
		}
	}
}

func TestImportCSVV1RejectsUnsafeControlText(t *testing.T) {
	for _, value := range []string{"legacy\x01memo", "bidi\u202etext", "bidi\u2066text", "line\u2028separator"} {
		setupCoreTestDB(t)
		content := "account,date,item,type,amount,memo\n" + value + ",2026-01-01,item,income,1,memo\n"
		if _, err := ImportCSV(content, "append"); err == nil {
			t.Fatalf("unsafe v1 value was accepted: %q", value)
		}
	}
}

func TestCSVTextCellEncodingIsCanonicalAndReversible(t *testing.T) {
	values := []string{
		"=1+1",
		"+cmd",
		"-cmd",
		"@cmd",
		" leading",
		"\tcmd",
		"\rcmd",
		"\ncmd",
		"\u00a0cmd",
		"\u200bcmd",
		"'literal",
		"normal",
		"",
	}
	for _, value := range values {
		encoded := encodeCSVTextCell(value)
		if needsCSVFormulaEscape(value) && !strings.HasPrefix(encoded, "'") {
			t.Errorf("dangerous value %q was not escaped", value)
		}
		decoded, err := decodeCSVTextCellV2(encoded)
		if err != nil {
			t.Errorf("decode %q: %v", encoded, err)
			continue
		}
		if decoded != value {
			t.Errorf("round trip = %q, want %q", decoded, value)
		}
	}
	if _, err := decodeCSVTextCellV2("=unescaped"); err == nil {
		t.Fatal("unescaped formula cell was accepted")
	}
	if _, err := decodeCSVTextCellV2("'unnecessary"); err == nil {
		t.Fatal("non-canonical escape was accepted")
	}
}

func TestBackupAndImportCSVV2PreservesEscapedTextExactly(t *testing.T) {
	setupCoreTestDB(t)
	wantAccount := "=Account"
	wantItem := "'literal"
	wantMemo := " \t@SUM(1,1)\nsecond line"
	if _, err := database.GetDB().Exec(
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, '2026-01-01', ?, 'expense', 123, -123, ?)`,
		wantAccount, wantItem, wantMemo,
	); err != nil {
		t.Fatal(err)
	}

	content, err := BackupToCSVV2()
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[0]) != 9 {
		t.Fatalf("records = %#v", records)
	}
	if records[0][8] != csvVersionHeader || records[1][8] != csvVersion2 {
		t.Fatalf("CSV version fields = %q, %q", records[0][8], records[1][8])
	}
	for _, index := range []int{1, 3, 7} {
		if !strings.HasPrefix(records[1][index], "'") {
			t.Errorf("column %d was not escaped: %q", index, records[1][index])
		}
	}

	setupCoreTestDB(t)
	if _, err := ImportCSV("\ufeff"+content, "append"); err != nil {
		t.Fatalf("ImportCSV v2: %v", err)
	}
	waitForSnapshotCount(t, 1)
	var gotAccount, gotItem, gotMemo string
	if err := database.GetDB().QueryRow("SELECT account, item, memo FROM transactions").Scan(&gotAccount, &gotItem, &gotMemo); err != nil {
		t.Fatal(err)
	}
	if gotAccount != wantAccount || gotItem != wantItem || gotMemo != wantMemo {
		t.Fatalf("round trip account=%q item=%q memo=%q", gotAccount, gotItem, gotMemo)
	}
}

func TestBackupAndImportCSVV2PreservesHistoricalTextOutsideNewWriteLimits(t *testing.T) {
	setupCoreTestDB(t)
	wantAccount := strings.Repeat("a", 300)
	wantItem := strings.Repeat("項", 300)
	wantMemo := "legacy 👨‍👩‍👧‍👦 memo"
	if _, err := database.GetDB().Exec(
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, '2026-01-01', ?, 'expense', 123, -123, ?)`,
		wantAccount, wantItem, wantMemo,
	); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSVV2()
	if err != nil {
		t.Fatal(err)
	}
	setupCoreTestDB(t)
	if _, err := ImportCSV(content, "append"); err != nil {
		t.Fatalf("historical v2 restore: %v", err)
	}
	var gotAccount, gotItem, gotMemo string
	if err := database.GetDB().QueryRow("SELECT account, item, memo FROM transactions").Scan(&gotAccount, &gotItem, &gotMemo); err != nil {
		t.Fatal(err)
	}
	if gotAccount != wantAccount || gotItem != wantItem || gotMemo != wantMemo {
		t.Fatalf("historical text changed: account=%d/%d item=%d/%d memo=%q/%q", len([]byte(gotAccount)), len([]byte(wantAccount)), len([]byte(gotItem)), len([]byte(wantItem)), gotMemo, wantMemo)
	}
}

func TestBackupAndImportCSVV3PreservesHistoricalSafeZWJ(t *testing.T) {
	setupCoreTestDB(t)
	wantAccount := "family 👨‍👩‍👧‍👦"
	if _, err := database.GetDB().Exec(
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, '2026-01-01', 'item', 'income', 123, 123, '')`,
		wantAccount,
	); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 3 || records[1][1] != "transaction_legacy" {
		t.Fatalf("safe historical format text was not archived: %#v", records)
	}
	setupCoreTestDB(t)
	if _, err := ImportCSV(content, "append"); err != nil {
		t.Fatalf("historical v3 restore: %v", err)
	}
	var got string
	if err := database.GetDB().QueryRow("SELECT account FROM transactions").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != wantAccount {
		t.Fatalf("historical v3 account changed: got %q want %q", got, wantAccount)
	}
}

func TestImportCSVV2RejectsUnsafeHistoricalText(t *testing.T) {
	for _, unsafe := range []string{"legacy\x01memo", "legacy\u202Ememo", "legacy\u2066memo", "legacy\u2028memo"} {
		setupCoreTestDB(t)
		content := "account,date,item,type,amount,memo,omni_money_csv_version\n" +
			"cash,2026-01-01,item,expense,1," + unsafe + ",2\n"
		if _, err := ImportCSV(content, "append"); err == nil {
			t.Fatalf("unsafe historical text was accepted: %q", unsafe)
		}
	}
}

func TestImportCSVV2RejectsUnknownVersionAndUnescapedFormula(t *testing.T) {
	for _, content := range []string{
		"account,date,item,type,amount,memo,omni_money_csv_version\ncash,2026-01-01,item,income,1,,3\n",
		"account,date,item,type,amount,memo,omni_money_csv_version\n=cmd,2026-01-01,item,income,1,,2\n",
	} {
		setupCoreTestDB(t)
		if _, err := ImportCSV(content, "append"); err == nil {
			t.Fatalf("unsafe v2 CSV was accepted: %q", content)
		}
	}
}

func TestLegacyCSVDoesNotDecodeLeadingApostrophe(t *testing.T) {
	setupCoreTestDB(t)
	content := "account,date,item,type,amount,memo\n'cash,2026-01-01,'item,income,1,'memo\n"
	if _, err := ImportCSV(content, "append"); err != nil {
		t.Fatal(err)
	}
	waitForSnapshotCount(t, 1)
	var account, item, memo string
	if err := database.GetDB().QueryRow("SELECT account, item, memo FROM transactions").Scan(&account, &item, &memo); err != nil {
		t.Fatal(err)
	}
	if account != "'cash" || item != "'item" || memo != "'memo" {
		t.Fatalf("legacy apostrophes changed: %q %q %q", account, item, memo)
	}
}
