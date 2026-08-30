package core

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"omni_money/backend/database"
)

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
