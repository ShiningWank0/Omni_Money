package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func TestTagSummaryThreeLevelGoldenPreservesOrderAndRatios(t *testing.T) {
	setupCoreTestDB(t)

	primaryID := insertTagSummaryTestTag(t, "primary", nil, 1)
	highID := insertTagSummaryTestTag(t, "high", &primaryID, 2)
	lowID := insertTagSummaryTestTag(t, "low", &primaryID, 2)
	grandchildID := insertTagSummaryTestTag(t, "grandchild", &highID, 3)
	secondaryID := insertTagSummaryTestTag(t, "secondary", nil, 1)

	linkAnalysisTestTag(t, insertTestTransaction(t, "cash", "2026-01-01", "root", "expense", 10, -10), primaryID)
	linkAnalysisTestTag(t, insertTestTransaction(t, "cash", "2026-01-02", "high", "expense", 20, -30), highID)
	linkAnalysisTestTag(t, insertTestTransaction(t, "cash", "2026-01-03", "grandchild", "expense", 30, -60), grandchildID)
	linkAnalysisTestTag(t, insertTestTransaction(t, "cash", "2026-01-04", "low", "expense", 5, -65), lowID)
	linkAnalysisTestTag(t, insertTestTransaction(t, "cash", "2026-01-05", "secondary", "expense", 40, -105), secondaryID)

	got, err := GetTagSummary("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	total := float64(105)
	want := []models.TagSummary{
		{
			TagID:   primaryID,
			TagName: "primary",
			Amount:  65,
			Count:   4,
			Ratio:   65 / total,
			Children: []models.TagSummary{
				{
					TagID:   highID,
					TagName: "high",
					Amount:  50,
					Count:   2,
					Ratio:   50 / total,
					Children: []models.TagSummary{
						{TagID: grandchildID, TagName: "grandchild", Amount: 30, Count: 1, Ratio: 30 / total},
					},
				},
				{TagID: lowID, TagName: "low", Amount: 5, Count: 1, Ratio: 5 / total},
			},
		},
		{TagID: secondaryID, TagName: "secondary", Amount: 40, Count: 1, Ratio: 40 / total},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tag summary mismatch\n got: %#v\nwant: %#v", got, want)
	}

	limited, err := AnalyzeTransactions(models.AnalysisRequest{Account: "cash", MaxTagSummaries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !limited.TagSummariesTruncated || len(limited.TagSummaries) != 1 {
		t.Fatalf("limited summary metadata = truncated:%v summaries:%#v", limited.TagSummariesTruncated, limited.TagSummaries)
	}
	root := limited.TagSummaries[0]
	if root.TagID != primaryID || root.Amount != 65 || root.Count != 4 || len(root.Children) != 1 {
		t.Fatalf("limited root lost full aggregate or DFS order: %#v", root)
	}
	if child := root.Children[0]; child.TagID != highID || child.Amount != 50 || child.Count != 2 || len(child.Children) != 0 {
		t.Fatalf("limited child = %#v", child)
	}
}

func TestTagSummaryPreservesParentChildDoubleCounting(t *testing.T) {
	setupCoreTestDB(t)

	parentID := insertTagSummaryTestTag(t, "parent", nil, 1)
	childID := insertTagSummaryTestTag(t, "child", &parentID, 2)
	transactionID := insertTestTransaction(t, "cash", "2026-02-01", "shared", "expense", 100, -100)
	linkAnalysisTestTag(t, transactionID, parentID)
	linkAnalysisTestTag(t, transactionID, childID)

	got, err := GetTagSummary("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Amount != 200 || got[0].Count != 2 || len(got[0].Children) != 1 {
		t.Fatalf("double-tag summary = %#v", got)
	}
	if child := got[0].Children[0]; child.Amount != 100 || child.Count != 1 || child.Ratio != 0.5 {
		t.Fatalf("double-tag child = %#v", child)
	}
}

func TestTagSummaryBudgetKeepsFullParentAggregate(t *testing.T) {
	const childCount = 600
	data := make([]tagSummaryData, 0, childCount+1)
	for i := 0; i < childCount; i++ {
		data = append(data, tagSummaryData{
			id:       int64(i + 2),
			name:     fmt.Sprintf("child-%03d", i),
			level:    2,
			parentID: validTagSummaryParent(1),
			amount:   int64(childCount - i),
			count:    1,
		})
	}
	data = append(data, tagSummaryData{id: 1, name: "root", level: 1})

	forest, err := buildTagSummaryForest(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	total, err := forest.rollup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, truncated, err := forest.materialize(context.Background(), total, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(got) != 1 || len(got[0].Children) != 499 {
		t.Fatalf("budget result = truncated:%v summaries:%#v child-count:%d", truncated, got, len(got[0].Children))
	}
	wantAmount := int64(childCount * (childCount + 1) / 2)
	if got[0].Amount != wantAmount || got[0].Count != childCount || got[0].Ratio != 1 {
		t.Fatalf("root aggregate = %#v, want amount=%d count=%d", got[0], wantAmount, childCount)
	}
	wantFirstChildRatio := float64(childCount) / float64(wantAmount)
	if got[0].Children[0].Ratio != wantFirstChildRatio {
		t.Fatalf(
			"first child ratio = %v, want %v using full untruncated total",
			got[0].Children[0].Ratio,
			wantFirstChildRatio,
		)
	}
	for i, child := range got[0].Children {
		if child.TagID != int64(i+2) || child.Amount != int64(childCount-i) {
			t.Fatalf("child[%d] = %#v", i, child)
		}
	}
}

func TestTagSummaryRejectsInvalidForests(t *testing.T) {
	tests := []struct {
		name string
		data []tagSummaryData
		want string
	}{
		{
			name: "duplicate",
			data: []tagSummaryData{{id: 1, level: 1}, {id: 1, level: 1}},
			want: "重複tag_id=1",
		},
		{
			name: "orphan",
			data: []tagSummaryData{{id: 1, level: 2, parentID: validTagSummaryParent(99)}},
			want: "orphan tag_id=1 parent_id=99",
		},
		{
			name: "self cycle",
			data: []tagSummaryData{{id: 1, level: 1, parentID: validTagSummaryParent(1)}},
			want: "cycle",
		},
		{
			name: "two node cycle",
			data: []tagSummaryData{
				{id: 1, level: 1, parentID: validTagSummaryParent(2)},
				{id: 2, level: 2, parentID: validTagSummaryParent(1)},
			},
			want: "cycle",
		},
		{
			name: "cycle reported after reachable child",
			data: []tagSummaryData{
				{id: 1, level: 1},
				{id: 2, level: 2, parentID: validTagSummaryParent(1)},
				{id: 3, level: 1, parentID: validTagSummaryParent(4)},
				{id: 4, level: 2, parentID: validTagSummaryParent(3)},
			},
			want: "cycle tag_id=3",
		},
		{name: "level zero", data: []tagSummaryData{{id: 1, level: 0}}, want: "level=0"},
		{name: "level four", data: []tagSummaryData{{id: 1, level: 4}}, want: "level=4"},
		{name: "root level two", data: []tagSummaryData{{id: 1, level: 2}}, want: "root tag_id=1"},
		{
			name: "level three under root",
			data: []tagSummaryData{
				{id: 1, level: 1},
				{id: 2, level: 3, parentID: validTagSummaryParent(1)},
			},
			want: "期待level=2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildTagSummaryForest(context.Background(), test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTagSummaryPreservesNonPositiveChildAndOverflowSemantics(t *testing.T) {
	t.Run("negative child is omitted from parent", func(t *testing.T) {
		forest, err := buildTagSummaryForest(context.Background(), []tagSummaryData{
			{id: 2, name: "child", level: 2, parentID: validTagSummaryParent(1), amount: -5, count: 1},
			{id: 1, name: "root", level: 1, amount: 10, count: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		total, err := forest.rollup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := forest.materialize(context.Background(), total, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Amount != 10 || got[0].Count != 1 || len(got[0].Children) != 0 {
			t.Fatalf("negative child changed parent: %#v", got)
		}
	})

	t.Run("node rollup overflow", func(t *testing.T) {
		forest, err := buildTagSummaryForest(context.Background(), []tagSummaryData{
			{id: 2, level: 2, parentID: validTagSummaryParent(1), amount: 1, count: 1},
			{id: 1, level: 1, amount: math.MaxInt64, count: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := forest.rollup(context.Background()); err == nil || !strings.Contains(err.Error(), "tag_id=1") {
			t.Fatalf("rollup overflow error = %v", err)
		}
	})

	t.Run("root total overflow", func(t *testing.T) {
		forest, err := buildTagSummaryForest(context.Background(), []tagSummaryData{
			{id: 1, level: 1, amount: math.MaxInt64, count: 1},
			{id: 2, level: 1, amount: 1, count: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := forest.rollup(context.Background()); err == nil || !strings.Contains(err.Error(), "タグ合計オーバーフロー") {
			t.Fatalf("root overflow error = %v", err)
		}
	})
}

func TestTagSummaryContextCancellationAndScanLimitCloseRows(t *testing.T) {
	setupCoreTestDB(t)
	db := database.GetDB()

	transaction, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare("INSERT INTO tags (name, level) VALUES (?, 1)")
	if err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	for i := 0; i < maxAITagSummaryScanNodes; i++ {
		if _, err := statement.Exec(fmt.Sprintf("scale-%05d", i)); err != nil {
			_ = statement.Close()
			_ = transaction.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	got, _, err := getTagSummaryFilteredContext(
		context.Background(), "", "", "", "", nil,
		tagSummaryOptions{maxScannedNodes: maxAITagSummaryScanNodes, maxMaterializedNodes: 500},
	)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("10,000 tag scan = summaries:%#v error:%v", got, err)
	}
	if _, err := db.Exec("INSERT INTO tags (name, level) VALUES ('scale-over-limit', 1)"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := getTagSummaryFilteredContext(
		context.Background(), "", "", "", "", nil,
		tagSummaryOptions{maxScannedNodes: maxAITagSummaryScanNodes, maxMaterializedNodes: 500},
	); err == nil || !strings.Contains(err.Error(), "内部上限10000件") {
		t.Fatalf("10,001 tag scan error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := getTagSummaryFilteredContext(canceled, "", "", "", "", nil, tagSummaryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	deadline, cancelDeadline := context.WithDeadline(context.Background(), testingDeadlineInPast())
	defer cancelDeadline()
	if _, err := AnalyzeTransactionsContext(deadline, models.AnalysisRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired deadline error = %v", err)
	}

	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("database unusable after cancellation: one=%d error=%v", one, err)
	}
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("database rows/connections still in use: %d", inUse)
	}
}

func TestTagSummaryForestRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := buildTagSummaryForest(ctx, []tagSummaryData{{id: 1, level: 1}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestEmptyTagSummaryIsNonNilJSONArray(t *testing.T) {
	setupCoreTestDB(t)
	got, err := GetTagSummary("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty summary = %#v", got)
	}
}

func BenchmarkBuildTagSummaryForest1000(b *testing.B) {
	benchmarkBuildTagSummaryForest(b, 1_000)
}

func BenchmarkBuildTagSummaryForest10000(b *testing.B) {
	benchmarkBuildTagSummaryForest(b, 10_000)
}

func benchmarkBuildTagSummaryForest(b *testing.B, size int) {
	b.Helper()
	data := make([]tagSummaryData, 0, size)
	for i := 1; i < size; i++ {
		data = append(data, tagSummaryData{
			id:       int64(i + 1),
			level:    2,
			parentID: validTagSummaryParent(1),
			amount:   1,
			count:    1,
		})
	}
	data = append(data, tagSummaryData{id: 1, level: 1})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		forest, err := buildTagSummaryForest(context.Background(), data)
		if err != nil {
			b.Fatal(err)
		}
		total, err := forest.rollup(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if _, truncated, err := forest.materialize(context.Background(), total, 500); err != nil || !truncated {
			b.Fatalf("materialize truncated=%v error=%v", truncated, err)
		}
	}
}

func insertTagSummaryTestTag(t *testing.T, name string, parentID *int64, level int) int64 {
	t.Helper()
	result, err := database.GetDB().Exec(
		"INSERT INTO tags (name, parent_id, level) VALUES (?, ?, ?)",
		name,
		parentID,
		level,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func validTagSummaryParent(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: true}
}

func testingDeadlineInPast() (deadlineTime time.Time) {
	return time.Unix(1, 0)
}
