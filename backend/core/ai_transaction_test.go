package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"omni_money/backend/database"
)

func aiTransactionIdentity(credentialID, key, request string, limit int, now time.Time) AITransactionIdentity {
	return AITransactionIdentity{
		CredentialID:          credentialID,
		IdempotencyKeySHA256:  sha256.Sum256([]byte(key)),
		RequestSHA256:         sha256.Sum256([]byte(request)),
		MaxTransactionsPerDay: limit,
		Now:                   now,
	}
}

func TestAddAITransactionIsAtomicAcrossIdempotencyAndQuota(t *testing.T) {
	setupCoreTestDB(t)
	settleAITransactionSnapshotsAtCleanup(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	request := transactionRequest("cash", "2026-08-09", "food", "expense", 100)
	identity := aiTransactionIdentity("agent-1", "first-key", "request-1", 1, now)

	first, err := AddAITransaction(context.Background(), request, identity)
	if err != nil {
		t.Fatalf("first AddAITransaction: %v", err)
	}
	if first.Replayed || first.Transaction.ID == 0 {
		t.Fatalf("first result = %#v", first)
	}
	replay, err := AddAITransaction(context.Background(), request, identity)
	if err != nil {
		t.Fatalf("replay AddAITransaction: %v", err)
	}
	if !replay.Replayed || replay.Transaction.ID != first.Transaction.ID {
		t.Fatalf("replay result = %#v, want ID %d", replay, first.Transaction.ID)
	}

	conflictIdentity := identity
	conflictIdentity.RequestSHA256 = sha256.Sum256([]byte("different-request"))
	if _, err := AddAITransaction(context.Background(), request, conflictIdentity); !errors.Is(err, ErrAIIdempotencyConflict) {
		t.Fatalf("key reuse error = %v, want conflict", err)
	}

	quotaIdentity := aiTransactionIdentity("agent-1", "second-key", "request-2", 1, now)
	if _, err := AddAITransaction(context.Background(), request, quotaIdentity); err == nil {
		t.Fatal("daily quota unexpectedly allowed a second create")
	} else {
		var quotaError *AIDailyQuotaExceededError
		if !errors.As(err, &quotaError) || quotaError.RetryAfterSeconds <= 0 {
			t.Fatalf("quota error = %#v", err)
		}
	}

	assertAIMutationCounts(t, 1, 1, 1)

	// The rejected claim was rolled back, so the same key can succeed in the
	// next UTC window instead of being left as an incomplete tombstone.
	quotaIdentity.Now = now.Add(24 * time.Hour)
	nextDay, err := AddAITransaction(context.Background(), request, quotaIdentity)
	if err != nil || nextDay.Replayed {
		t.Fatalf("next-day create = %#v, err=%v", nextDay, err)
	}
	assertAIMutationCounts(t, 2, 2, 2)
}

func TestAddAITransactionRollsBackClaimAndQuotaWithLedgerFailure(t *testing.T) {
	setupCoreTestDB(t)
	settleAITransactionSnapshotsAtCleanup(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	request := transactionRequest("cash", "2026-08-09", "food", "expense", 100)
	request.Tags = []int64{999999}
	identity := aiTransactionIdentity("agent-1", "rollback-key", "invalid-tag", 10, now)

	if _, err := AddAITransaction(context.Background(), request, identity); err == nil {
		t.Fatal("invalid tag unexpectedly committed")
	}
	assertAIMutationCounts(t, 0, 0, 0)

	request.Tags = nil
	identity.RequestSHA256 = sha256.Sum256([]byte("valid-request"))
	if _, err := AddAITransaction(context.Background(), request, identity); err != nil {
		t.Fatalf("rolled-back key could not be retried: %v", err)
	}
	assertAIMutationCounts(t, 1, 1, 1)
}

func TestAddAITransactionConcurrentReplayCreatesOneLedgerRow(t *testing.T) {
	setupCoreTestDB(t)
	settleAITransactionSnapshotsAtCleanup(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	request := transactionRequest("cash", "2026-08-09", "food", "expense", 100)
	identity := aiTransactionIdentity("agent-concurrent", "shared-key", "shared-request", 100, now)

	const workers = 16
	start := make(chan struct{})
	results := make(chan *AITransactionResult, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := AddAITransaction(context.Background(), request, identity)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent AddAITransaction: %v", err)
	}
	var transactionID int64
	newCount := 0
	for result := range results {
		if transactionID == 0 {
			transactionID = result.Transaction.ID
		}
		if result.Transaction.ID != transactionID {
			t.Errorf("transaction ID = %d, want %d", result.Transaction.ID, transactionID)
		}
		if !result.Replayed {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("new results = %d, want 1", newCount)
	}
	assertAIMutationCounts(t, 1, 1, 1)
}

func TestAddAITransactionConcurrentDistinctKeysCannotExceedQuota(t *testing.T) {
	setupCoreTestDB(t)
	settleAITransactionSnapshotsAtCleanup(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	request := transactionRequest("cash", "2026-08-09", "food", "expense", 100)

	const (
		workers = 16
		quota   = 5
	)
	start := make(chan struct{})
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			identity := aiTransactionIdentity(
				"agent-quota",
				fmt.Sprintf("key-%d", index),
				fmt.Sprintf("request-%d", index),
				quota,
				now,
			)
			_, err := AddAITransaction(context.Background(), request, identity)
			errorsChannel <- err
		}(i)
	}
	close(start)
	wait.Wait()
	close(errorsChannel)

	successes := 0
	quotaFailures := 0
	for err := range errorsChannel {
		if err == nil {
			successes++
			continue
		}
		var quotaError *AIDailyQuotaExceededError
		if errors.As(err, &quotaError) {
			quotaFailures++
			continue
		}
		t.Errorf("unexpected concurrent quota error: %v", err)
	}
	if successes != quota || quotaFailures != workers-quota {
		t.Fatalf("successes=%d quota failures=%d, want %d/%d", successes, quotaFailures, quota, workers-quota)
	}
	assertAIMutationCounts(t, quota, quota, quota)
}

func TestAddAITransactionIdempotencySurvivesDatabaseReopen(t *testing.T) {
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "persistent.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	settleAITransactionSnapshotsAtCleanup(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	request := transactionRequest("cash", "2026-08-09", "food", "expense", 100)
	identity := aiTransactionIdentity("agent-persistent", "persistent-key", "persistent-request", 1, now)
	first, err := AddAITransaction(context.Background(), request, identity)
	if err != nil {
		t.Fatal(err)
	}

	// Serialize with the asynchronous snapshot worker before closing the shared
	// database connection used by the test process.
	if _, err := database.CreateSnapshot(""); err != nil {
		t.Fatal(err)
	}
	database.CloseDB()
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	replay, err := AddAITransaction(context.Background(), request, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Transaction.ID != first.Transaction.ID {
		t.Fatalf("reopen replay = %#v, want ID %d", replay, first.Transaction.ID)
	}
	newIdentity := aiTransactionIdentity("agent-persistent", "new-key", "new-request", 1, now)
	if _, err := AddAITransaction(context.Background(), request, newIdentity); err == nil {
		t.Fatal("daily quota was lost after database reopen")
	} else {
		var quotaError *AIDailyQuotaExceededError
		if !errors.As(err, &quotaError) {
			t.Fatalf("reopen quota error = %v", err)
		}
	}
	assertAIMutationCounts(t, 1, 1, 1)
}

// AutoSnapshot is intentionally asynchronous. Wait for the coalescing worker
// to become idle before TempDir cleanup so it cannot write into a directory
// that the testing package is concurrently removing.
func settleAITransactionSnapshotsAtCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		lastCount := -1
		stableSince := time.Time{}
		for time.Now().Before(deadline) {
			snapshots, err := database.ListSnapshots("")
			if err == nil && len(snapshots) > 0 {
				if len(snapshots) != lastCount {
					lastCount = len(snapshots)
					stableSince = time.Now()
				} else if time.Since(stableSince) >= 300*time.Millisecond {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("AI snapshot worker did not settle before cleanup")
	})
}

func assertAIMutationCounts(t *testing.T, transactions, idempotencyRows, usage int) {
	t.Helper()
	db := database.GetDB()
	checks := []struct {
		query string
		want  int
	}{
		{"SELECT COUNT(*) FROM transactions", transactions},
		{"SELECT COUNT(*) FROM ai_transaction_idempotency", idempotencyRows},
		{"SELECT COALESCE(SUM(successful_creates), 0) FROM ai_daily_transaction_usage", usage},
	}
	for _, check := range checks {
		var got int
		if err := db.QueryRow(check.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("%s = %d, want %d", check.query, got, check.want)
		}
	}
}
