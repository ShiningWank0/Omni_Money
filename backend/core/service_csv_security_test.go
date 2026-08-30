package core

import (
	"encoding/csv"
	"strings"
	"testing"

	"omni_money/backend/database"
)

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

	content, err := BackupToCSV()
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

	if _, err := ImportCSV("\ufeff"+content, "replace"); err != nil {
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
	wantMemo := "legacy\u200b memo"
	if _, err := database.GetDB().Exec(
		`INSERT INTO transactions (account, date, item, type, amount, balance, memo) VALUES (?, '2026-01-01', ?, 'expense', 123, -123, ?)`,
		wantAccount, wantItem, wantMemo,
	); err != nil {
		t.Fatal(err)
	}
	content, err := BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportCSV(content, "replace"); err != nil {
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

func TestImportCSVV2RejectsUnknownVersionAndUnescapedFormula(t *testing.T) {
	for _, content := range []string{
		"account,date,item,type,amount,memo,omni_money_csv_version\ncash,2026-01-01,item,income,1,,3\n",
		"account,date,item,type,amount,memo,omni_money_csv_version\n=cmd,2026-01-01,item,income,1,,2\n",
	} {
		setupCoreTestDB(t)
		if _, err := ImportCSV(content, "replace"); err == nil {
			t.Fatalf("unsafe v2 CSV was accepted: %q", content)
		}
	}
}

func TestLegacyCSVDoesNotDecodeLeadingApostrophe(t *testing.T) {
	setupCoreTestDB(t)
	content := "account,date,item,type,amount,memo\n'cash,2026-01-01,'item,income,1,'memo\n"
	if _, err := ImportCSV(content, "replace"); err != nil {
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
