package core

import (
	"encoding/csv"
	"reflect"
	"strings"
	"testing"

	"omni_money/backend/models"
)

func attackInputTransaction() models.TransactionRequest {
	return models.TransactionRequest{
		Account: "cash'); DELETE FROM transactions; --", Date: "2026-01-02", Type: "expense", Amount: 37,
		Item: "item'); DROP TABLE transactions; --", Memo: "memo', amount=999 WHERE 1=1; --",
	}
}

func requireAttackInputRow(t *testing.T, service *Service, req models.TransactionRequest) models.TransactionResponse {
	t.Helper()
	rows, err := service.GetTransactions(req.Account, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("literal account lookup: rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.Account != req.Account || row.Item != req.Item || row.Memo != req.Memo || row.Amount != req.Amount || row.Type != req.Type {
		t.Fatalf("attack text did not round-trip: got=%#v request=%#v", row, req)
	}
	return row
}

func TestSQLInjectionInputStaysLiteralAcrossTransactionWritesAndQueries(t *testing.T) {
	_, service := openCoreTestService(t, "attack-input")
	sentinelReq := models.TransactionRequest{Account: "unrelated", Date: "2026-01-01", Item: "untouched", Type: "income", Amount: 123, Memo: "private sentinel"}
	if _, err := service.AddTransaction(sentinelReq); err != nil {
		t.Fatal(err)
	}
	sentinel := requireAttackInputRow(t, service, sentinelReq)
	req := attackInputTransaction()
	created, err := service.AddTransaction(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := requireAttackInputRow(t, service, req); got.ID != created.ID {
		t.Fatalf("created ID=%d, retrieved ID=%d", created.ID, got.ID)
	}

	// These contain no LIKE wildcard characters. A successful result here would
	// indicate SQL predicate interpretation rather than the intentional %/_ search semantics.
	for _, payload := range []string{"' OR 1=1 --", "' UNION SELECT 1,2,3,4,5,6,7,8,9 --", "'; DELETE FROM transactions; --"} {
		for _, query := range []struct{ account, search string }{{payload, ""}, {"", payload}} {
			rows, err := service.GetTransactions(query.account, query.search)
			if err != nil || len(rows) != 0 {
				t.Fatalf("predicate escaped for %#v: rows=%#v err=%v", query, rows, err)
			}
		}
	}
	for _, search := range []string{req.Item, req.Memo} {
		rows, err := service.GetTransactions("", search)
		if err != nil || len(rows) != 1 || rows[0].ID != created.ID {
			t.Fatalf("literal search %q: rows=%#v err=%v", search, rows, err)
		}
	}

	req.Account = "updated' OR '1'='1"
	req.Item = "updated', amount=0 WHERE 1=1; --"
	req.Memo = "updated'); DROP TABLE tags; --"
	req.Amount = 41
	if _, err := service.UpdateTransaction(created.ID, req); err != nil {
		t.Fatal(err)
	}
	if got := requireAttackInputRow(t, service, req); got.ID != created.ID {
		t.Fatalf("update changed ID: %d", got.ID)
	}
	if got := requireAttackInputRow(t, service, sentinelReq); !reflect.DeepEqual(got, sentinel) {
		t.Fatalf("unrelated transaction changed: before=%#v after=%#v", sentinel, got)
	}
	rows, err := service.GetTransactions("", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("unexpected table contents: %#v, %v", rows, err)
	}
}

func TestSQLInjectionInputStaysLiteralInSettingsAndTags(t *testing.T) {
	_, service := openCoreTestService(t, "attack-settings")
	if err := service.SaveBankAccountSettings([]string{"unrelated bank"}); err != nil {
		t.Fatal(err)
	}
	settings := []string{"card'); DROP TABLE settings; --", "' OR 1=1 --"}
	if err := service.SaveCreditCardSettings(settings); err != nil {
		t.Fatal(err)
	}
	if got, err := service.GetCreditCardSettings(); err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("settings did not round-trip: %#v, %v", got, err)
	}
	if got, err := service.GetBankAccountSettings(); err != nil || !reflect.DeepEqual(got, []string{"unrelated bank"}) {
		t.Fatalf("unrelated setting changed: %#v, %v", got, err)
	}
	sentinel, err := service.CreateTag("untouched tag", nil)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := service.CreateTag("tag'); DELETE FROM tags; --", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.GetTags(); err != nil || len(got) != 2 {
		t.Fatalf("create changed tag set: %#v, %v", got, err)
	} else {
		for _, stored := range got {
			if stored.ID == tag.ID && stored.Name != tag.Name {
				t.Fatalf("created tag text changed: %#v", stored)
			}
		}
	}
	const renamed = "renamed' WHERE 1=1; --"
	if err := service.UpdateTag(tag.ID, renamed); err != nil {
		t.Fatal(err)
	}
	tags, err := service.GetTags()
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%#v err=%v", tags, err)
	}
	want := map[int64]string{sentinel.ID: sentinel.Name, tag.ID: renamed}
	for _, stored := range tags {
		if name, ok := want[stored.ID]; !ok || stored.Name != name {
			t.Fatalf("tag mutation escaped its row: %#v", tags)
		}
	}
}

func TestSQLInjectionInputStaysLiteralThroughCSVAppend(t *testing.T) {
	for _, format := range []string{"legacy", "v3"} {
		t.Run(format, func(t *testing.T) {
			_, source := openCoreTestService(t, "csv-source")
			_, target := openCoreTestService(t, "csv-target")
			req := attackInputTransaction()
			if _, err := source.AddTransaction(req); err != nil {
				t.Fatal(err)
			}
			const tagName = "tag'); DROP TABLE tags; --"
			tag, err := source.CreateTag(tagName, nil)
			if err != nil {
				t.Fatal(err)
			}
			row := requireAttackInputRow(t, source, req)
			if err := source.AddTransactionTags(row.ID, []int64{tag.ID}); err != nil {
				t.Fatal(err)
			}
			settings := []string{"card'); DELETE FROM settings; --"}
			if err := source.SaveCreditCardSettings(settings); err != nil {
				t.Fatal(err)
			}
			sentinelReq := models.TransactionRequest{Account: "unrelated", Date: "2026-01-01", Item: "untouched", Type: "income", Amount: 123}
			if _, err := target.AddTransaction(sentinelReq); err != nil {
				t.Fatal(err)
			}
			sentinel := requireAttackInputRow(t, target, sentinelReq)
			if err := target.SaveBankAccountSettings([]string{"unrelated bank"}); err != nil {
				t.Fatal(err)
			}
			content, err := source.BackupToCSV()
			if err != nil {
				t.Fatal(err)
			}
			if format == "legacy" {
				var buffer strings.Builder
				writer := csv.NewWriter(&buffer)
				if err := writer.WriteAll([][]string{{"account", "date", "item", "type", "amount", "memo"}, {req.Account, req.Date, req.Item, req.Type, "37", req.Memo}}); err != nil {
					t.Fatal(err)
				}
				content = buffer.String()
			}
			if count, err := target.ImportCSV(content, "append"); err != nil || count != 1 {
				t.Fatalf("import: count=%d err=%v", count, err)
			}
			imported := requireAttackInputRow(t, target, req)
			if format == "v3" {
				if len(imported.Tags) != 1 || imported.Tags[0].Name != tagName {
					t.Fatalf("imported tags=%#v", imported.Tags)
				}
				if got, err := target.GetCreditCardSettings(); err != nil || !reflect.DeepEqual(got, settings) {
					t.Fatalf("imported settings=%#v err=%v", got, err)
				}
			}
			if got := requireAttackInputRow(t, target, sentinelReq); !reflect.DeepEqual(got, sentinel) {
				t.Fatalf("CSV changed unrelated transaction: before=%#v after=%#v", sentinel, got)
			}
			if got, err := target.GetBankAccountSettings(); err != nil || !reflect.DeepEqual(got, []string{"unrelated bank"}) {
				t.Fatalf("CSV changed unrelated setting: %#v, %v", got, err)
			}
			rows, err := target.GetTransactions("", "")
			if err != nil || len(rows) != 2 {
				t.Fatalf("CSV changed table contents: %#v, %v", rows, err)
			}
		})
	}
}
