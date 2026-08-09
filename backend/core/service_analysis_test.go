package core

import (
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
