package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"omni_money/backend/models"
)

func subscriptionTestNow(value string) time.Time {
	date, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return date
}

func subscriptionTestRule(t *testing.T, s *Service, currency string) *SubscriptionRule {
	t.Helper()
	source, err := s.AddTransaction(models.TransactionRequest{Account: "card", Date: "2026-01-31", Item: "Streaming", Type: "expense", Amount: 1000, Memo: "original"})
	if err != nil {
		t.Fatal(err)
	}
	amount := "1000"
	if currency != "JPY" {
		amount = "9.99"
	}
	rule, err := s.saveSubscriptionAt("", SubscriptionInput{SourceTransactionID: source.ID, Account: "card", Item: "Streaming", Memo: "template", Currency: currency, OriginalAmount: amount, Interval: "monthly", AnchorDay: 31, AnchorMonth: 1, Timezone: "Asia/Tokyo", NextDate: "2026-02-28", Enabled: true}, subscriptionTestNow("2026-02-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func noFX(t *testing.T) func(context.Context, string, string) (subscriptionRate, error) {
	return func(context.Context, string, string) (subscriptionRate, error) {
		t.Error("JPY requested an external rate")
		return subscriptionRate{}, errors.New("unexpected")
	}
}

func TestSubscriptionCatchupCalendarConfirmationAndProvenance(t *testing.T) {
	_, s := openCoreTestService(t, "subscriptions")
	rule := subscriptionTestRule(t, s, "JPY")
	calendar, err := s.SubscriptionCalendar("2031-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar) != 1 || calendar[0].Date != "2031-02-28" || calendar[0].Transaction != nil {
		t.Fatalf("future=%+v", calendar)
	}
	before, _ := s.GetTransactions("", "")
	if len(before) != 1 || before[0].Subscription != nil {
		t.Fatalf("source changed=%+v", before)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-05-31T14:00:00Z"), noFX(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 4 || result.HasMore {
		t.Fatalf("sync=%+v", result)
	}
	transactions, err := s.GetTransactions("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 5 || transactions[4].BalanceExact != "-5000" {
		t.Fatalf("transactions=%+v", transactions)
	}
	for index, date := range []string{"2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31"} {
		transaction := transactions[index+1]
		if transaction.Date[:10] != date || transaction.Subscription == nil || !transaction.Subscription.Unconfirmed || transaction.Subscription.RuleID != rule.ID {
			t.Fatalf("transaction=%+v", transaction)
		}
	}
	first := transactions[1]
	if _, err := s.UpdateTransaction(first.ID, models.TransactionRequest{Account: first.Account, Date: first.Date[:10], Item: first.Item, Type: "expense", Amount: 0}); err == nil {
		t.Fatal("invalid save accepted")
	}
	still, _ := s.GetTransactions("", "")
	if !still[1].Subscription.Unconfirmed {
		t.Fatal("failed save confirmed")
	}
	updated, err := s.UpdateTransaction(first.ID, models.TransactionRequest{Account: first.Account, Date: first.Date[:10], Item: first.Item, Type: first.Type, Amount: first.Amount, Memo: first.Memo})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Subscription == nil || updated.Subscription.Unconfirmed {
		t.Fatalf("unchanged save=%+v", updated)
	}
	updated, err = s.UpdateTransaction(first.ID, models.TransactionRequest{Account: "renamed", Date: "2026-06-03", Item: "edited", Type: "expense", Amount: 123, Memo: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.SubscriptionTransactions(rule.ID, "renamed", "edited", "2026-06-01", "2026-06-30", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Transactions) != 1 || history.Transactions[0].ID != first.ID || history.Transactions[0].Subscription.Unconfirmed {
		t.Fatalf("edited origin=%+v", history)
	}
	calendar, err = s.SubscriptionCalendar("2026-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar) != 1 || calendar[0].Transaction.ID != first.ID || calendar[0].Transaction.Item != "edited" {
		t.Fatalf("calendar actual=%+v", calendar)
	}
	result, err = s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-05-31T14:00:00Z"), noFX(t))
	if err != nil || result.Created != 0 {
		t.Fatalf("retry=%+v %v", result, err)
	}
}

func TestSubscriptionConcurrentCatchupAndDeletionDoNotDuplicate(t *testing.T) {
	instance, s := openCoreTestService(t, "concurrent")
	rule := subscriptionTestRule(t, s, "JPY")
	s.snapshot = nil
	var wg sync.WaitGroup
	errorsOut := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-03-31T12:00:00Z"), noFX(t))
			errorsOut <- err
		}()
	}
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Transactions) != 2 {
		t.Fatalf("duplicate occurrences=%+v", history)
	}
	for _, transaction := range history.Transactions {
		if err := s.DeleteTransaction(transaction.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-03-31T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := instance.DB().QueryRow("SELECT COUNT(*) FROM subscription_occurrences WHERE transaction_id IS NULL").Scan(&count); err != nil || count != 2 {
		t.Fatalf("tombstones=%d error=%v", count, err)
	}
	transactions, _ := s.GetTransactions("", "")
	if len(transactions) != 1 {
		t.Fatalf("deleted payments recreated=%+v", transactions)
	}
	calendar, _ := s.SubscriptionCalendar("2026-02")
	if len(calendar) != 0 {
		t.Fatalf("deleted actual shown as forecast=%+v", calendar)
	}
}

func TestSubscriptionStopResumeScheduleBoundaryAndStaleEdits(t *testing.T) {
	_, s := openCoreTestService(t, "stop")
	rule := subscriptionTestRule(t, s, "JPY")
	rule.Enabled = false
	stopped, err := s.saveSubscriptionAt(rule.ID, rule.SubscriptionInput, subscriptionTestNow("2026-02-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-05-31T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 0 {
		t.Fatalf("stopped=%+v %v", result, err)
	}
	if _, err := s.saveSubscriptionAt(rule.ID, rule.SubscriptionInput, subscriptionTestNow("2026-05-31T12:00:00Z")); !errors.Is(err, ErrSubscriptionConflict) {
		t.Fatalf("stale edit=%v", err)
	}
	stopped.Enabled = true
	if _, err := s.saveSubscriptionAt(rule.ID, stopped.SubscriptionInput, subscriptionTestNow("2026-05-31T12:00:00Z")); err == nil {
		t.Fatal("resume silently accepted stopped backlog")
	}
	stopped.NextDate = "2026-06-30"
	if _, err := s.saveSubscriptionAt(rule.ID, stopped.SubscriptionInput, subscriptionTestNow("2026-05-31T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	result, err = s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-06-30T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 1 {
		t.Fatalf("resume=%+v %v", result, err)
	}
}

func TestSubscriptionDateBoundariesAndTimezone(t *testing.T) {
	for _, test := range []struct {
		date, interval string
		day, month     int
		want           string
	}{
		{"2026-01-31", "monthly", 31, 1, "2026-02-28"}, {"2026-02-28", "monthly", 31, 1, "2026-03-31"},
		{"2026-12-31", "monthly", 31, 12, "2027-01-31"}, {"2027-02-28", "yearly", 29, 2, "2028-02-29"}, {"2028-02-29", "yearly", 29, 2, "2029-02-28"},
	} {
		t.Run(test.date+test.interval, func(t *testing.T) {
			date, err := subscriptionNextDate(SubscriptionRule{SubscriptionInput: SubscriptionInput{NextDate: test.date, Interval: test.interval, AnchorDay: test.day, AnchorMonth: test.month}})
			if err != nil || date != test.want {
				t.Fatalf("date=%s err=%v", date, err)
			}
		})
	}
	instant := subscriptionTestNow("2026-02-28T15:00:00Z")
	for zone, want := range map[string]string{"Asia/Tokyo": "2026-03-01", "America/Los_Angeles": "2026-02-28", "UTC": "2026-02-28"} {
		got, err := subscriptionToday(zone, instant)
		if err != nil || got != want {
			t.Fatalf("%s=%s %v", zone, got, err)
		}
	}
	if _, err := subscriptionToday("Local", instant); err == nil {
		t.Fatal("host timezone accepted")
	}
}

func TestSubscriptionFXFailureExactConversionAndPartialRetry(t *testing.T) {
	_, s := openCoreTestService(t, "fx")
	rule := subscriptionTestRule(t, s, "USD")
	lookup := func(_ context.Context, currency, date string) (subscriptionRate, error) {
		if currency != "USD" {
			t.Fatal(currency)
		}
		if date == "2026-03-31" {
			return subscriptionRate{}, errors.New("down")
		}
		return subscriptionRate{Date: date, Rate: "150.5"}, nil
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-03-31T12:00:00Z"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || len(result.Errors) != 1 {
		t.Fatalf("partial=%+v", result)
	}
	history, _ := s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if len(history.Transactions) != 1 || history.Transactions[0].AmountExact != "1503" || history.Transactions[0].Subscription.Rate != "150.5" || history.Transactions[0].Subscription.OriginalAmount != "9.99" {
		t.Fatalf("FX history=%+v", history)
	}
	result, err = s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-03-31T12:00:00Z"), func(_ context.Context, _, date string) (subscriptionRate, error) {
		return subscriptionRate{Date: date, Rate: "151.00"}, nil
	})
	if err != nil || result.Created != 1 {
		t.Fatalf("retry=%+v %v", result, err)
	}
	history, _ = s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if len(history.Transactions) != 2 || history.Transactions[0].AmountExact != "1503" || history.Transactions[1].AmountExact != "1508" {
		t.Fatalf("FX repriced/duplicated=%+v", history)
	}
}

func TestSubscriptionGenerationRollbackLeaseAndBoundedContinuation(t *testing.T) {
	instance, s := openCoreTestService(t, "rollback")
	rule := subscriptionTestRule(t, s, "JPY")
	if _, err := instance.DB().Exec("CREATE TRIGGER fail_occurrence BEFORE INSERT ON subscription_occurrences BEGIN SELECT RAISE(ABORT,'failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t)); err == nil {
		t.Fatal("injected failure ignored")
	}
	transactions, _ := s.GetTransactions("", "")
	if len(transactions) != 1 {
		t.Fatal("partial ledger transaction committed")
	}
	if _, err := instance.DB().Exec("DROP TRIGGER fail_occurrence"); err != nil {
		t.Fatal(err)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2030-01-31T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 24 || !result.HasMore {
		t.Fatalf("bounded pass=%+v %v", result, err)
	}
	valid := true
	guarded, err := NewGuardedService(instance, func() bool { return valid })
	if err != nil {
		t.Fatal(err)
	}
	valid = false
	if _, err := guarded.SyncSubscriptions(context.Background()); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("expired lease generated: %v", err)
	}
	if _, err := guarded.SubscriptionCalendar("2030-01"); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("expired lease read: %v", err)
	}
	history, err := s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if err != nil || len(history.Transactions) != 24 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestSubscriptionSaveQueueConfirmsOnlyAtomicCompletion(t *testing.T) {
	instance, s := openCoreTestService(t, "save-queue")
	subscriptionTestRule(t, s, "JPY")
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	transactions, _ := s.GetTransactions("", "")
	target := transactions[1]
	request := models.TransactionRequest{Account: target.Account, Date: target.Date[:10], Item: target.Item, Type: target.Type, Amount: target.Amount, Memo: target.Memo}
	requestID := "8b82fb9b-5cc4-4de9-a98a-2a2cf72cd4df"
	if _, err := s.AcceptTransactionSave(requestID, target.ID, request); err != nil {
		t.Fatal(err)
	}
	if err := instance.WaitForLedgerWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := s.TransactionSaveStatus(requestID)
	if err != nil || status.State != "completed" || status.Transaction.Subscription == nil || status.Transaction.Subscription.Unconfirmed {
		t.Fatalf("completed=%+v %v", status, err)
	}
	if _, err := s.AcceptTransactionSave(requestID, target.ID, request); err != nil {
		t.Fatal(err)
	}
	transactions, _ = s.GetTransactions("", "")
	if transactions[1].Subscription.Unconfirmed {
		t.Fatal("completed save not persisted")
	}
}

func TestSubscriptionCSVReplacePausesRulesAndSnapshotRestoresRecords(t *testing.T) {
	instance, s := openCoreTestService(t, "restore")
	rule := subscriptionTestRule(t, s, "JPY")
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	archive, err := s.BackupToCSV()
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.WaitForAutoSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshotDir := t.TempDir()
	snapshot, err := instance.CreateSnapshot(snapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCSV(archive, "replace"); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.Subscriptions()
	if len(rules) != 1 || rules[0].Enabled {
		t.Fatalf("replace left active=%+v", rules)
	}
	history, _ := s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if len(history.Transactions) != 0 {
		t.Fatalf("CSV imported forged provenance=%+v", history)
	}
	if err := instance.WaitForAutoSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := instance.RestoreSnapshot(snapshotDir, filepath.Base(snapshot)); err != nil {
		t.Fatal(err)
	}
	restored, err := NewService(instance)
	if err != nil {
		t.Fatal(err)
	}
	history, err = restored.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if err != nil || len(history.Transactions) != 1 || !history.Transactions[0].Subscription.Unconfirmed {
		t.Fatalf("snapshot lost records=%+v %v", history, err)
	}
	result, err := restored.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 0 {
		t.Fatalf("restore duplicated=%+v %v", result, err)
	}
}

func TestSubscriptionCandidatesNeedApprovalAndRememberRejection(t *testing.T) {
	_, s := openCoreTestService(t, "suggestions")
	for month := 1; month <= 3; month++ {
		if _, err := s.AddTransaction(models.TransactionRequest{Account: "cash", Date: fmt.Sprintf("2026-%02d-15", month), Item: "Music", Type: "expense", Amount: 500}); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := s.SubscriptionCandidates()
	if err != nil || len(candidates) != 1 || candidates[0].Interval != "monthly" {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	rules, _ := s.Subscriptions()
	if len(rules) != 0 {
		t.Fatal("suggestion created a rule")
	}
	if err := s.DismissSubscriptionCandidate(candidates[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	candidates, err = s.SubscriptionCandidates()
	if err != nil || len(candidates) != 0 {
		t.Fatalf("rejected candidate returned=%+v %v", candidates, err)
	}
}

func TestSubscriptionStopWinsAgainstAnInFlightRateLookup(t *testing.T) {
	_, s := openCoreTestService(t, "stop-while-fetching")
	rule := subscriptionTestRule(t, s, "USD")
	started, proceed := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), func(_ context.Context, _, date string) (subscriptionRate, error) {
			close(started)
			<-proceed
			return subscriptionRate{Date: date, Rate: "150"}, nil
		})
		finished <- err
	}()
	<-started
	rule.Enabled = false
	_, err := s.saveSubscriptionAt(rule.ID, rule.SubscriptionInput, subscriptionTestNow("2026-02-28T12:00:00Z"))
	close(proceed)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	history, _ := s.SubscriptionTransactions(rule.ID, "", "", "", "", 0)
	if len(history.Transactions) != 0 {
		t.Fatal("stopped rule generated after network response")
	}
}

func TestSubscriptionSaveCompletionFailureRollsBackConfirmation(t *testing.T) {
	instance, s := openCoreTestService(t, "atomic-confirmation")
	subscriptionTestRule(t, s, "JPY")
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	transactions, _ := s.GetTransactions("", "")
	target := transactions[1]
	if _, err := instance.DB().Exec("CREATE TRIGGER block_completed BEFORE UPDATE ON transaction_save_requests WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT,'blocked'); END"); err != nil {
		t.Fatal(err)
	}
	requestID := "6425a5b7-bf0d-4a2f-bb61-5ef6dd4196a3"
	request := models.TransactionRequest{Account: target.Account, Date: target.Date[:10], Item: target.Item, Type: target.Type, Amount: target.Amount, Memo: target.Memo}
	if _, err := s.AcceptTransactionSave(requestID, target.ID, request); err != nil {
		t.Fatal(err)
	}
	if err := instance.WaitForLedgerWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	transactions, _ = s.GetTransactions("", "")
	if !transactions[1].Subscription.Unconfirmed {
		t.Fatal("uncompleted save cleared confirmation")
	}
	if _, err := instance.DB().Exec("DROP TRIGGER block_completed"); err != nil {
		t.Fatal(err)
	}
	instance.WakeLedgerWorker()
	if err := instance.WaitForLedgerWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := s.TransactionSaveStatus(requestID)
	if err != nil || status.State != "completed" || status.Transaction.Subscription.Unconfirmed {
		t.Fatalf("retry=%+v %v", status, err)
	}
}

func TestSubscriptionServicesNeverReadOrMutateAnotherVault(t *testing.T) {
	_, first := openCoreTestService(t, "first")
	_, second := openCoreTestService(t, "second")
	rule := subscriptionTestRule(t, first, "JPY")
	if _, err := second.SubscriptionTransactions(rule.ID, "", "", "", "", 0); err == nil {
		t.Fatal("read another vault's history")
	}
	if _, err := second.SaveSubscription(rule.ID, rule.SubscriptionInput); err == nil {
		t.Fatal("modified another vault's rule")
	}
	if _, err := second.SyncSubscriptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	rules, _ := second.Subscriptions()
	if len(rules) != 0 {
		t.Fatal("borrowed another vault's rules")
	}
	transactions, _ := first.GetTransactions("", "")
	if len(transactions) != 1 {
		t.Fatal("another user's sync generated transactions")
	}
}

func TestSubscriptionScheduleEditCannotReissueTheSameMonthOrYear(t *testing.T) {
	_, s := openCoreTestService(t, "period-boundary")
	rule := subscriptionTestRule(t, s, "JPY")
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-28T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.Subscriptions()
	input := rules[0].SubscriptionInput
	input.AnchorDay = 31
	input.NextDate = "2026-03-31"
	changed, err := s.saveSubscriptionAt(rule.ID, input, subscriptionTestNow("2026-03-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-03-31T12:00:00Z"), noFX(t)); err != nil {
		t.Fatal(err)
	}
	rules, _ = s.Subscriptions()
	input = rules[0].SubscriptionInput
	input.Interval = "yearly"
	input.AnchorDay = 31
	input.AnchorMonth = 12
	input.NextDate = "2026-12-31"
	if _, err := s.saveSubscriptionAt(changed.ID, input, subscriptionTestNow("2026-04-01T00:00:00Z")); err == nil {
		t.Fatal("same year's payment reissued after cycle change")
	}
	input.Interval = "monthly"
	input.AnchorMonth = 3
	input.AnchorDay = 31
	input.NextDate = "2026-03-31"
	if _, err := s.saveSubscriptionAt(changed.ID, input, subscriptionTestNow("2026-03-31T12:00:00Z")); err == nil {
		t.Fatal("processed month reissued")
	}
	// The manual source also consumes its initial period without being marked
	// generated or becoming part of a subscription's history.
	source, err := s.AddTransaction(models.TransactionRequest{Account: "cash", Date: "2026-06-01", Item: "other", Type: "expense", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	input.SourceTransactionID = source.ID
	input.Account = "cash"
	input.Item = "other"
	input.NextDate = "2026-06-30"
	input.AnchorDay = 30
	input.Revision = 0
	if _, err := s.saveSubscriptionAt("", input, subscriptionTestNow("2026-06-02T12:00:00Z")); err == nil {
		t.Fatal("source month duplicated")
	}
}

func TestSubscriptionAnnualCatchupUsesFullAnnualAmountsAndLeapAnchor(t *testing.T) {
	_, s := openCoreTestService(t, "annual")
	s.snapshot = nil
	source, err := s.AddTransaction(models.TransactionRequest{Account: "cash", Date: "2024-02-29", Item: "Annual", Type: "expense", Amount: 12000})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.saveSubscriptionAt("", SubscriptionInput{SourceTransactionID: source.ID, Account: "cash", Item: "Annual", Currency: "JPY", OriginalAmount: "12000", Interval: "yearly", AnchorDay: 29, AnchorMonth: 2, Timezone: "Asia/Tokyo", NextDate: "2025-02-28", Enabled: true}, subscriptionTestNow("2024-03-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2028-02-29T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 4 {
		t.Fatalf("annual=%+v %v", result, err)
	}
	transactions, _ := s.GetTransactions("", "")
	if len(transactions) != 5 || transactions[4].Date != "2028-02-29" || transactions[4].BalanceExact != "-60000" {
		t.Fatalf("annual transactions=%+v", transactions)
	}
}

func TestSubscriptionCatchupAtTheRuleLimitHasBoundedWork(t *testing.T) {
	instance, s := openCoreTestService(t, "capacity")
	s.snapshot = nil
	tx, err := beginLedgerWrite(context.Background(), instance.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index := 1; index <= maxSubscriptionRules; index++ {
		item := fmt.Sprintf("subscription-%d", index)
		if _, err := tx.Exec("INSERT INTO transactions(id,account,date,item,type,amount,balance,memo) VALUES(?,'cash','2026-01-01',?,'expense',100,0,'')", index, item); err != nil {
			t.Fatal(err)
		}
		definition, _ := json.Marshal(subscriptionDefinition{SourceDate: "2026-01-01", SubscriptionInput: SubscriptionInput{SourceTransactionID: int64(index), Account: "cash", Item: item, Currency: "JPY", OriginalAmount: "100", Interval: "monthly", AnchorDay: 1, AnchorMonth: 1, Timezone: "UTC", NextDate: "2026-02-01", Enabled: true}})
		id := fmt.Sprintf("6425a5b7-bf0d-4a2f-bb61-%012x", index)
		if _, err := tx.Exec("INSERT INTO subscription_rules(id,revision,enabled,next_sequence,next_date,definition) VALUES(?,1,1,1,'2026-02-01',?)", id, string(definition)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("2026-02-01T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 24 || !result.HasMore {
		t.Fatalf("capacity=%+v %v", result, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SyncSubscriptions(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pass=%v", err)
	}
}

func TestSubscriptionFinalSupportedPaymentCommitsBeforeStopping(t *testing.T) {
	_, s := openCoreTestService(t, "last-supported-payment")
	s.snapshot = nil
	source, err := s.AddTransaction(models.TransactionRequest{Account: "cash", Date: "9997-02-28", Item: "Annual", Type: "expense", Amount: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.saveSubscriptionAt("", SubscriptionInput{SourceTransactionID: source.ID, Account: "cash", Item: "Annual", Currency: "JPY", OriginalAmount: "100", Interval: "yearly", AnchorDay: 28, AnchorMonth: 2, Timezone: "UTC", NextDate: "9998-02-28", Enabled: true}, subscriptionTestNow("9997-03-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.syncSubscriptionsAt(context.Background(), subscriptionTestNow("9998-02-28T12:00:00Z"), noFX(t))
	if err != nil || result.Created != 1 || result.HasMore {
		t.Fatalf("last payment=%+v %v", result, err)
	}
	rules, err := s.Subscriptions()
	if err != nil || len(rules) != 1 || rules[0].Enabled {
		t.Fatalf("limit state=%+v %v", rules, err)
	}
}
