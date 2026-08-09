package database

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotWorkerCoalescesChangesAndSavesFinalState(t *testing.T) {
	var state atomic.Int64
	state.Store(1)

	var active atomic.Int64
	var maxActive atomic.Int64
	var calls atomic.Int64
	var capturedMu sync.Mutex
	var capturedStates []int64

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondFinished := make(chan struct{})
	var releaseOnce sync.Once

	worker := newSnapshotWorker(func() {
		currentActive := active.Add(1)
		for {
			previousMax := maxActive.Load()
			if currentActive <= previousMax || maxActive.CompareAndSwap(previousMax, currentActive) {
				break
			}
		}

		callNumber := calls.Add(1)
		capturedState := state.Load()
		if callNumber == 1 {
			close(firstStarted)
			<-releaseFirst
		}

		capturedMu.Lock()
		capturedStates = append(capturedStates, capturedState)
		capturedMu.Unlock()
		active.Add(-1)

		if callNumber == 2 {
			close(secondFinished)
		}
	})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseFirst) })
		worker.close()
	})

	worker.request()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first snapshot did not start")
	}

	// 最初の保存実行中に状態を変更し、通知を集中させる。
	// すべての通知は1件へまとめられるが、変更後の状態は次回実行で保存される必要がある。
	state.Store(2)
	for i := 0; i < 32; i++ {
		worker.request()
	}
	releaseOnce.Do(func() { close(releaseFirst) })

	select {
	case <-secondFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot for the final state did not finish")
	}

	if got := maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent snapshots = %d, want 1", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("snapshot executions = %d, want 2 (initial and coalesced final state)", got)
	}

	capturedMu.Lock()
	defer capturedMu.Unlock()
	if len(capturedStates) != 2 || capturedStates[0] != 1 || capturedStates[1] != 2 {
		t.Fatalf("captured states = %v, want [1 2]", capturedStates)
	}
}

func TestCreateTablesCreatesQueryOptimizationIndexes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "indexes.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(CloseDB)

	indexNames := []string{
		"idx_transactions_account_date_id",
		"idx_transaction_links_child_id",
	}
	for _, indexName := range indexNames {
		var count int
		if err := GetDB().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?",
			indexName,
		).Scan(&count); err != nil {
			t.Fatalf("query index %q: %v", indexName, err)
		}
		if count != 1 {
			t.Errorf("index %q count = %d, want 1", indexName, count)
		}
	}
}
