package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

const saveTestID = "7616712d-b63e-4dfa-9fa2-707b6b3c0d9a"
const saveTestID2 = "7616712d-b63e-4dfa-9fa2-707b6b3c0d9b"

func saveTestRequest() models.TransactionRequest {
	return models.TransactionRequest{Account: "cash", Date: "2026-10-04", Item: "lunch", Type: "expense", Amount: 100}
}

func waitSave(t *testing.T, s *Service, id string) *TransactionSave {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result, err := s.TransactionSaveStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		if result.State != "pending" {
			return result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("save did not finish")
	return nil
}

func TestSaveRequestIDsDeduplicateReplayButNotIdenticalContent(t *testing.T) {
	instance, service := openCoreTestService(t, "save-ids")
	request := saveTestRequest()
	var group sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			// SQLite may reject a deferred transaction upgrading concurrently;
			// replaying the same durable ID must remain safe in that case too.
			for attempt := 0; attempt < 20; attempt++ {
				_, err := service.AcceptTransactionSave(saveTestID, 0, request)
				if err == nil {
					errorsSeen <- nil
					return
				}
				if !errors.Is(err, ErrSaveStorageUnavailable) {
					errorsSeen <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
			errorsSeen <- errors.New("concurrent receipt did not converge")
		})
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := waitSave(t, service, saveTestID)
	if first.State != "completed" {
		t.Fatalf("first result: %+v", first)
	}
	if _, err := service.AcceptTransactionSave(saveTestID2, 0, request); err != nil {
		t.Fatal(err)
	}
	second := waitSave(t, service, saveTestID2)
	if second.State != "completed" || first.Transaction.ID == second.Transaction.ID {
		t.Fatal("identical legitimate transactions were deduplicated")
	}
	request.Amount++
	if _, err := service.AcceptTransactionSave(saveTestID, 0, request); !errors.Is(err, ErrSaveRequestConflict) {
		t.Fatalf("ID reuse changed intent: %v", err)
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 2 {
		t.Fatalf("rows=%d error=%v", count, err)
	}
}

func TestAcceptedImageSaveSurvivesRequestLeaseRelease(t *testing.T) {
	instance, _ := openCoreTestService(t, "save-after-logout")
	for range cap(imageDecodeSlots) {
		imageDecodeSlots <- struct{}{}
	}
	var releaseSlots sync.Once
	release := func() {
		releaseSlots.Do(func() {
			for range cap(imageDecodeSlots) {
				<-imageDecodeSlots
			}
		})
	}
	defer release()
	var live atomic.Bool
	live.Store(true)
	service, err := NewGuardedService(instance, live.Load)
	if err != nil {
		t.Fatal(err)
	}
	request := saveTestRequest()
	request.Images = []models.TransactionImageRequest{imageRequest("receipt.png", "image/png", encodePNG(t))}
	receipt, err := service.AcceptTransactionSave(saveTestID, 0, request)
	if err != nil || receipt.State != "pending" {
		t.Fatalf("receipt waited for image processing: %+v %v", receipt, err)
	}
	var rows int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("blocked image save already mutated ledger")
	}
	live.Store(false)
	if _, err := service.GetAccounts(); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatal("released request retained access")
	}
	release()
	fresh, err := NewService(instance)
	if err != nil {
		t.Fatal(err)
	}
	result := waitSave(t, fresh, saveTestID)
	if result.State != "completed" {
		t.Fatalf("admitted save cancelled: %+v", result)
	}
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_images").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("image not saved: %d %v", rows, err)
	}
}

func TestPendingSaveResumesAfterDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable-save.db")
	instance, err := database.OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process that durably acknowledged the receipt and stopped
	// before its worker started. No queue processing is installed here.
	service := &Service{instance: instance, db: instance.DB()}
	if _, err := service.AcceptTransactionSave(saveTestID, 0, saveTestRequest()); err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.OpenPlainInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh, err := NewService(reopened)
	if err != nil {
		t.Fatal(err)
	}
	result := waitSave(t, fresh, saveTestID)
	if result.State != "completed" {
		t.Fatalf("restart lost receipt: %+v", result)
	}
	if _, err := fresh.AcceptTransactionSave(saveTestID, 0, saveTestRequest()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.WaitForLedgerWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart replay duplicated save: %d %v", count, err)
	}
}

func TestQueuedUpdateCommitsResultWithImagesAndTagsOnce(t *testing.T) {
	instance, service := openCoreTestService(t, "save-update")
	created, err := service.AddTransaction(saveTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	request := saveTestRequest()
	request.Item = "dinner"
	request.Amount = 200
	request.NewTagPaths = []string{"food"}
	request.Images = []models.TransactionImageRequest{imageRequest("receipt.png", "image/png", encodePNG(t))}
	if _, err := service.AcceptTransactionSave(saveTestID, created.ID, request); err != nil {
		t.Fatal(err)
	}
	result := waitSave(t, service, saveTestID)
	if result.State != "completed" || result.Transaction.Item != "dinner" || len(result.Transaction.Tags) != 1 {
		t.Fatalf("bad result: %+v", result)
	}
	if _, err := service.AcceptTransactionSave(saveTestID, created.ID, request); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transaction_images").Scan(&count); err != nil || count != 1 {
		t.Fatalf("update replay duplicated image: %d %v", count, err)
	}
}

func TestFailedSaveRollsBackLedgerAndRetainsFailedResult(t *testing.T) {
	instance, service := openCoreTestService(t, "save-failure")
	request := saveTestRequest()
	request.Tags = []int64{99999}
	if _, err := service.AcceptTransactionSave(saveTestID, 0, request); err != nil {
		t.Fatal(err)
	}
	result := waitSave(t, service, saveTestID)
	if result.State != "failed" || result.Error == "" {
		t.Fatalf("validation failure marked successful: %+v", result)
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed job partially mutated ledger: %d %v", count, err)
	}
	if _, err := service.AcceptTransactionSave(saveTestID, 0, request); err != nil {
		t.Fatal(err)
	}
	if replay := waitSave(t, service, saveTestID); replay.State != "failed" {
		t.Fatal("failed retry started a new operation")
	}
	notices, err := service.TransactionSaveNotices()
	if err != nil || len(notices) != 1 || notices[0].Request == nil || notices[0].Request.Item != request.Item {
		t.Fatalf("failed input was discarded: %+v %v", notices, err)
	}
	if err := service.DismissFailedTransactionSave(saveTestID); err != nil {
		t.Fatal(err)
	}
	notices, err = service.TransactionSaveNotices()
	if err != nil || len(notices) != 0 {
		t.Fatalf("dismissed input retained in notices: %+v %v", notices, err)
	}
	if _, err := service.AcceptTransactionSave(saveTestID, 0, request); err != nil {
		t.Fatal(err)
	}
	if replay := waitSave(t, service, saveTestID); replay.State != "failed" {
		t.Fatal("dismissing notice lost the deduplication tombstone")
	}
}

func TestPendingSaveNoticeOmitsImageDataAndCannotBeDismissed(t *testing.T) {
	instance, _ := openCoreTestService(t, "save-notice")
	for range cap(imageDecodeSlots) {
		imageDecodeSlots <- struct{}{}
	}
	defer func() {
		for range cap(imageDecodeSlots) {
			<-imageDecodeSlots
		}
	}()
	service, err := NewService(instance)
	if err != nil {
		t.Fatal(err)
	}
	request := saveTestRequest()
	request.Images = []models.TransactionImageRequest{imageRequest("receipt.png", "image/png", encodePNG(t))}
	if _, err := service.AcceptTransactionSave(saveTestID, 0, request); err != nil {
		t.Fatal(err)
	}
	notices, err := service.TransactionSaveNotices()
	if err != nil || len(notices) != 1 || notices[0].State != "pending" || notices[0].Request == nil || len(notices[0].Request.Images) != 0 {
		t.Fatalf("bad pending notice: %+v %v", notices, err)
	}
	if err := service.DismissFailedTransactionSave(saveTestID); err == nil {
		t.Fatal("pending input was discarded")
	}
}
