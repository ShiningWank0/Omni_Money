package core

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"omni_money/backend/database"
)

func setupCardReconciliation(t *testing.T) *Service {
	t.Helper()
	setupCoreTestDB(t)
	writeStringSliceSetting(t, "credit_card_items", []string{"card", "second card"})
	writeStringSliceSetting(t, "bank_account_items", []string{"bank"})
	service, err := newLegacyService()
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func reconciliationTransaction(t *testing.T, account, kind string, amount int64) int64 {
	t.Helper()
	return insertTestTransaction(t, account, "2026-01-01", "payment", kind, amount, 0)
}

func TestCardReconciliationAllowsManyCardsAndRefundsWithoutBlockingMismatch(t *testing.T) {
	service := setupCardReconciliation(t)
	bank := reconciliationTransaction(t, "bank", "expense", 450)
	first := reconciliationTransaction(t, "card", "expense", 300)
	second := reconciliationTransaction(t, "second card", "expense", 200)
	refund := reconciliationTransaction(t, "card", "income", 50)
	for _, card := range []int64{first, second, refund} {
		if err := service.AddTransactionLink(bank, card); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.GetCardReconciliation(bank)
	if err != nil || !result.Applicable || !result.Matched || result.CardTotalExact != "450" || result.DifferenceExact != "0" || len(result.Cards) != 3 {
		t.Fatalf("matched reconciliation = %+v, %v", result, err)
	}
	if _, err := service.UpdateTransaction(first, transactionRequest("card", "2026-01-01", "changed", "expense", 301)); err != nil {
		t.Fatalf("amount mismatch blocked an edit: %v", err)
	}
	result, err = service.GetCardReconciliation(bank)
	if err != nil || result.Matched || result.CardTotalExact != "451" || result.DifferenceExact != "1" {
		t.Fatalf("mismatch = %+v, %v", result, err)
	}
	if err := service.RemoveTransactionLink(bank, refund); err != nil {
		t.Fatal(err)
	}
	result, err = service.GetCardReconciliation(bank)
	if err != nil || result.CardTotalExact != "501" || result.DifferenceExact != "51" {
		t.Fatalf("unlink did not update reconciliation: %+v, %v", result, err)
	}
}

func TestCardCannotBeAssignedToMultipleBanksFromEitherEndpoint(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			service := setupCardReconciliation(t)
			card := reconciliationTransaction(t, "card", "expense", 100)
			bankA := reconciliationTransaction(t, "bank", "expense", 100)
			bankB := reconciliationTransaction(t, "bank", "expense", 200)
			otherCard := reconciliationTransaction(t, "second card", "expense", 200)
			if err := service.AddTransactionLink(bankA, card); err != nil {
				t.Fatal(err)
			}
			a, b := card, bankB
			if reverse {
				a, b = b, a
			}
			if err := service.AddTransactionLink(a, b); err == nil {
				t.Fatal("a second bank was allowed")
			}
			if err := service.AddTransactionLink(bankB, otherCard); err != nil {
				t.Fatalf("independent settlement group was rejected: %v", err)
			}
			if err := service.AddTransactionLink(bankA, otherCard); err == nil {
				t.Fatal("many-to-many linking was allowed")
			}
			if err := service.AddTransactionLink(card, bankA); err != nil {
				t.Fatalf("existing link retry: %v", err)
			}
		})
	}
}

func TestStagedCardAssignmentIsAtomicAndCanMoveToAnotherBank(t *testing.T) {
	service := setupCardReconciliation(t)
	card := reconciliationTransaction(t, "card", "expense", 100)
	bankA := reconciliationTransaction(t, "bank", "expense", 100)
	bankB := reconciliationTransaction(t, "bank", "expense", 200)
	request := transactionRequest("card", "2026-01-01", "must roll back", "expense", 300)
	request.LinkAddIDs = []int64{bankA, bankB}
	if _, err := service.UpdateTransaction(card, request); err == nil {
		t.Fatal("two staged bank links were allowed")
	}
	links, err := service.GetTransactionLinks(card)
	if err != nil || len(links) != 0 {
		t.Fatalf("failed update left partial links: %+v, %v", links, err)
	}
	var amount int64
	if err := database.GetDB().QueryRow("SELECT amount FROM transactions WHERE id = ?", card).Scan(&amount); err != nil || amount != 100 {
		t.Fatalf("failed update changed amount=%d: %v", amount, err)
	}
	if err := service.AddTransactionLink(card, bankA); err != nil {
		t.Fatal(err)
	}
	bankRequest := transactionRequest("bank", "2026-01-01", "bank-side assignment", "expense", 200)
	bankRequest.LinkAddIDs = []int64{card}
	if _, err := service.UpdateTransaction(bankB, bankRequest); err == nil {
		t.Fatal("bank-side staged assignment bypassed the restriction")
	}
	request.LinkRemoveIDs, request.LinkAddIDs = []int64{bankA}, []int64{bankB}
	if _, err := service.UpdateTransaction(card, request); err != nil {
		t.Fatalf("atomic reassignment was rejected: %v", err)
	}
	links, err = service.GetTransactionLinks(card)
	if err != nil || len(links) != 1 || links[0].ID != bankB {
		t.Fatalf("reassignment = %+v, %v", links, err)
	}
}

func TestConcurrentCardAssignmentsCannotBothCommit(t *testing.T) {
	service := setupCardReconciliation(t)
	card := reconciliationTransaction(t, "card", "expense", 100)
	bankA := reconciliationTransaction(t, "bank", "expense", 100)
	bankB := reconciliationTransaction(t, "bank", "expense", 100)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, bank := range []int64{bankA, bankB} {
		workers.Add(1)
		go func(bank int64) {
			defer workers.Done()
			<-start
			results <- service.AddTransactionLink(card, bank)
		}(bank)
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	links, err := service.GetTransactionLinks(card)
	if successes != 1 || err != nil || len(links) != 1 {
		t.Fatalf("concurrent assignments: successes=%d, links=%+v, err=%v", successes, links, err)
	}
}

func insertLegacyCardLink(t *testing.T, first, second int64) {
	t.Helper()
	if first > second {
		first, second = second, first
	}
	if _, err := database.GetDB().Exec("INSERT INTO transaction_links(parent_id, child_id) VALUES (?, ?)", first, second); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCardConflictsArePreservedAndCanBeRepaired(t *testing.T) {
	service := setupCardReconciliation(t)
	card := reconciliationTransaction(t, "card", "expense", 100)
	bankA := reconciliationTransaction(t, "bank", "expense", 100)
	bankB := reconciliationTransaction(t, "bank", "expense", 100)
	insertLegacyCardLink(t, card, bankA)
	insertLegacyCardLink(t, card, bankB)
	result, err := service.GetCardReconciliation(bankA)
	if err != nil || !result.HasConflicts || len(result.Cards) != 1 || len(result.Cards[0].LinkedBankIDs) != 2 {
		t.Fatalf("legacy conflict not reported: %+v, %v", result, err)
	}
	if _, err := service.UpdateTransaction(card, transactionRequest("card", "2026-01-01", "edit without changing links", "expense", 101)); err != nil {
		t.Fatalf("legacy conflict blocked unrelated editing: %v", err)
	}
	links, _ := service.GetTransactionLinks(card)
	if len(links) != 2 {
		t.Fatal("legacy links were silently deleted")
	}
	if err := service.RemoveTransactionLink(card, bankB); err != nil {
		t.Fatal(err)
	}
	result, err = service.GetCardReconciliation(bankA)
	if err != nil || result.HasConflicts || result.CardTotalExact != "101" {
		t.Fatalf("legacy conflict was not repaired: %+v, %v", result, err)
	}
}

func TestCardReconciliationIsScopedAndUsesExactUnboundedTotals(t *testing.T) {
	service := setupCardReconciliation(t)
	bank := reconciliationTransaction(t, "bank", "expense", 1)
	for range 2 {
		card := reconciliationTransaction(t, "card", "expense", 1)
		if _, err := database.GetDB().Exec("INSERT INTO transaction_archive_amounts(transaction_id, amount) VALUES (?, ?)", card, int64(math.MaxInt64)); err != nil {
			t.Fatal(err)
		}
		if err := service.AddTransactionLink(card, bank); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.GetCardReconciliation(bank)
	if err != nil || result.CardTotalExact != "18446744073709551614" || result.DifferenceExact != "18446744073709551613" {
		t.Fatalf("exact total overflowed: %+v, %v", result, err)
	}
	cash := reconciliationTransaction(t, "cash", "expense", 1)
	result, err = service.GetCardReconciliation(cash)
	if err != nil || result.Applicable || result.CardTotalExact != "" {
		t.Fatalf("reconciliation applied to an unregistered fund item: %+v, %v", result, err)
	}
}

func TestCSVImportCannotCreateMultipleBankAssignmentsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"append", "replace"} {
		t.Run(mode, func(t *testing.T) {
			service := setupCardReconciliation(t)
			card := reconciliationTransaction(t, "card", "expense", 100)
			bankA := reconciliationTransaction(t, "bank", "expense", 100)
			bankB := reconciliationTransaction(t, "bank", "expense", 100)
			insertLegacyCardLink(t, card, bankA)
			insertLegacyCardLink(t, card, bankB)
			archive, err := service.BackupToCSV()
			if err != nil {
				t.Fatal(err)
			}
			if err := service.RemoveTransactionLink(card, bankB); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ImportCSV(archive, mode); err == nil || !strings.Contains(err.Error(), "銀行取引は1件まで") {
				t.Fatalf("invalid CSV import result: %v", err)
			}
			var count int
			if err := database.GetDB().QueryRow("SELECT count(*) FROM transactions").Scan(&count); err != nil || count != 3 {
				t.Fatalf("CSV rollback changed transactions: count=%d, %v", count, err)
			}
			links, err := service.GetTransactionLinks(card)
			if err != nil || len(links) != 1 || links[0].ID != bankA {
				t.Fatalf("CSV rollback changed existing links: %+v, %v", links, err)
			}
		})
	}
}

func TestSettingsCannotReorientExistingLinksIntoAMultiBankAssignment(t *testing.T) {
	service := setupCardReconciliation(t)
	bank := reconciliationTransaction(t, "bank", "expense", 100)
	for range 2 {
		card := reconciliationTransaction(t, "card", "expense", 50)
		if err := service.AddTransactionLink(bank, card); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SaveBankAccountSettings([]string{"bank", "card"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveCreditCardSettings([]string{"bank"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveBankAccountSettings([]string{"card"}); err == nil {
		t.Fatal("settings reclassification bypassed the one-bank rule")
	}
	items, _ := service.GetBankAccountSettings()
	if len(items) != 2 || items[0] != "bank" {
		t.Fatalf("failed settings mutation was not rolled back: %v", items)
	}
}

func TestNewCardLinksRejectAmbiguousClassification(t *testing.T) {
	service := setupCardReconciliation(t)
	card := reconciliationTransaction(t, "card", "expense", 100)
	bank := reconciliationTransaction(t, "bank", "expense", 100)
	if err := service.SaveCreditCardSettings([]string{"card", "bank"}); err != nil {
		t.Fatal(err)
	}
	if err := service.AddTransactionLink(card, bank); err == nil {
		t.Fatal("a new link with an ambiguous card/bank classification was accepted")
	}
}
