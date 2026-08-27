package core

import (
	"fmt"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestAnalyzeTransactionsAppliesAccountAndTagFiltersToTagSummaries(t *testing.T) {
	setupCoreTestDB(t)

	foodTagID := insertAnalysisTestTag(t, "food")
	travelTagID := insertAnalysisTestTag(t, "travel")
	sharedTagID := insertAnalysisTestTag(t, "shared")

	cashFood := insertTestTransaction(t, "cash", "2026-07-01", "lunch", "expense", 100, -100)
	bankFood := insertTestTransaction(t, "bank", "2026-07-01", "dinner", "expense", 200, -200)
	cashTravel := insertTestTransaction(t, "cash", "2026-07-02", "train", "expense", 300, -400)
	cashFoodShared := insertTestTransaction(t, "cash", "2026-07-02", "cafe", "expense", 400, -800)

	linkAnalysisTestTag(t, cashFood, foodTagID)
	linkAnalysisTestTag(t, bankFood, foodTagID)
	linkAnalysisTestTag(t, cashTravel, travelTagID)
	linkAnalysisTestTag(t, cashFoodShared, foodTagID)
	linkAnalysisTestTag(t, cashFoodShared, sharedTagID)

	accountResult, err := AnalyzeTransactions(models.AnalysisRequest{Account: "cash"})
	if err != nil {
		t.Fatalf("AnalyzeTransactions(account) failed: %v", err)
	}
	assertAnalysisTagSummary(t, accountResult.TagSummaries, "food", 500, 2)
	assertAnalysisTagSummary(t, accountResult.TagSummaries, "travel", 300, 1)
	assertAnalysisTagSummary(t, accountResult.TagSummaries, "shared", 400, 1)

	tagResult, err := AnalyzeTransactions(models.AnalysisRequest{Account: "cash", TagIDs: []int64{foodTagID}})
	if err != nil {
		t.Fatalf("AnalyzeTransactions(account+tag) failed: %v", err)
	}
	if tagResult.Count != 2 || tagResult.TotalExpense != 500 {
		t.Fatalf("filtered analysis count=%d expense=%d, want 2 and 500", tagResult.Count, tagResult.TotalExpense)
	}
	if len(tagResult.Transactions) != 0 || tagResult.ReturnedCount != 0 {
		t.Fatalf("summary-only analysis leaked transactions: %#v", tagResult.Transactions)
	}
	assertAnalysisTagSummary(t, tagResult.TagSummaries, "food", 500, 2)
	assertAnalysisTagSummary(t, tagResult.TagSummaries, "shared", 400, 1)
	assertAnalysisTagSummaryAbsent(t, tagResult.TagSummaries, "travel")

	emptyResult, err := AnalyzeTransactions(models.AnalysisRequest{TagIDs: []int64{999999}})
	if err != nil {
		t.Fatalf("AnalyzeTransactions(unknown tag) failed: %v", err)
	}
	if emptyResult.Count != 0 || len(emptyResult.TagSummaries) != 0 {
		t.Fatalf("unknown tag result = count:%d summaries:%#v", emptyResult.Count, emptyResult.TagSummaries)
	}
}

func TestAnalyzeTransactionsDetailsUseStableBoundedPagination(t *testing.T) {
	setupCoreTestDB(t)
	wantIDs := make(map[int64]struct{})
	for i := 0; i < 5; i++ {
		id := insertTestTransaction(t, "cash", fmt.Sprintf("2026-07-%02d", i+1), fmt.Sprintf("item-%d", i), "expense", int64(i+1)*100, -100)
		wantIDs[id] = struct{}{}
	}
	insertTestTransaction(t, "bank", "2026-07-03", "out-of-scope", "expense", 999, -999)

	request := models.AnalysisRequest{
		Account:             "cash",
		StartDate:           "2026-07-01",
		EndDate:             "2026-07-31",
		IncludeTransactions: true,
		Limit:               2,
	}
	seen := make(map[int64]struct{})
	for {
		response, err := AnalyzeTransactions(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.Count != 5 || response.ReturnedCount < 1 || response.ReturnedCount > 2 {
			t.Fatalf("page metadata = count:%d returned:%d", response.Count, response.ReturnedCount)
		}
		for _, transaction := range response.Transactions {
			if transaction.Account != "cash" || transaction.Memo != "" {
				t.Fatalf("detail leaked account or memo: %#v", transaction)
			}
			if _, duplicate := seen[transaction.ID]; duplicate {
				t.Fatalf("duplicate transaction across pages: %d", transaction.ID)
			}
			seen[transaction.ID] = struct{}{}
		}
		if response.NextCursor == "" {
			break
		}
		request.Cursor = response.NextCursor
	}
	if len(seen) != len(wantIDs) {
		t.Fatalf("seen IDs = %#v, want %#v", seen, wantIDs)
	}
	for id := range wantIDs {
		if _, ok := seen[id]; !ok {
			t.Fatalf("transaction %d missing from pagination", id)
		}
	}

	request.Cursor = "invalid"
	if _, err := AnalyzeTransactions(request); err == nil {
		t.Fatal("invalid cursor was accepted")
	}
	request.Cursor = ""
	request.Limit = maxAIAnalysisLimit + 1
	if _, err := AnalyzeTransactions(request); err == nil {
		t.Fatal("oversized detail limit was accepted")
	}
}

func TestTruncateTagSummariesBoundsNestedResponse(t *testing.T) {
	summaries := []models.TagSummary{
		{TagID: 1, Children: []models.TagSummary{{TagID: 2}, {TagID: 3}}},
		{TagID: 4},
	}
	remaining := 2
	got := truncateTagSummaries(summaries, &remaining)
	if len(got) != 1 || got[0].TagID != 1 || len(got[0].Children) != 1 || got[0].Children[0].TagID != 2 {
		t.Fatalf("truncated summaries = %#v", got)
	}
	var count int
	countTagSummaries(got, &count)
	if count != 2 || remaining != 0 {
		t.Fatalf("count=%d remaining=%d, want 2,0", count, remaining)
	}
}

func insertAnalysisTestTag(t *testing.T, name string) int64 {
	t.Helper()
	result, err := database.GetDB().Exec("INSERT INTO tags (name, level) VALUES (?, 1)", name)
	if err != nil {
		t.Fatalf("insert tag failed: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("tag LastInsertId failed: %v", err)
	}
	return id
}

func linkAnalysisTestTag(t *testing.T, transactionID, tagID int64) {
	t.Helper()
	if _, err := database.GetDB().Exec(
		"INSERT INTO transaction_tags (transaction_id, tag_id) VALUES (?, ?)",
		transactionID,
		tagID,
	); err != nil {
		t.Fatalf("link tag failed: %v", err)
	}
}

func assertAnalysisTagSummary(t *testing.T, summaries []models.TagSummary, name string, amount int64, count int) {
	t.Helper()
	for _, summary := range summaries {
		if summary.TagName == name {
			if summary.Amount != amount || summary.Count != count {
				t.Fatalf("tag %q amount=%d count=%d, want %d and %d", name, summary.Amount, summary.Count, amount, count)
			}
			return
		}
	}
	t.Fatalf("tag %q not found in %#v", name, summaries)
}

func assertAnalysisTagSummaryAbsent(t *testing.T, summaries []models.TagSummary, name string) {
	t.Helper()
	for _, summary := range summaries {
		if summary.TagName == name {
			t.Fatalf("tag %q unexpectedly found in %#v", name, summaries)
		}
	}
}
