package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"omni_money/backend/database"
	"omni_money/backend/models"
)

func openCoreTestService(t *testing.T, name string) (*database.Instance, *Service) {
	t.Helper()
	instance, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatalf("OpenPlainInstance(%s): %v", name, err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Errorf("Close(%s): %v", name, err)
		}
	})
	service, err := NewService(instance)
	if err != nil {
		t.Fatalf("NewService(%s): %v", name, err)
	}
	return instance, service
}

func TestServicesKeepParallelVaultDataAndAIStateIsolated(t *testing.T) {
	instanceA, serviceA := openCoreTestService(t, "vault-a")
	instanceB, serviceB := openCoreTestService(t, "vault-b")
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)

	type operation struct {
		service  *Service
		account  string
		item     string
		identity AITransactionIdentity
	}
	operations := []operation{
		{
			service:  serviceA,
			account:  "vault-a-sentinel",
			item:     "only-a",
			identity: aiTransactionIdentity("shared-credential", "key-a", "request-a", 1, now),
		},
		{
			service:  serviceB,
			account:  "vault-b-sentinel",
			item:     "only-b",
			identity: aiTransactionIdentity("shared-credential", "key-b", "request-b", 1, now),
		},
	}

	start := make(chan struct{})
	errorsChannel := make(chan error, len(operations))
	var wait sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			request := models.TransactionRequest{
				Account: operation.account,
				Date:    "2026-08-28",
				Item:    operation.item,
				Type:    "expense",
				Amount:  100,
			}
			_, err := operation.service.AddAITransaction(context.Background(), request, operation.identity)
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("parallel AddAITransaction: %v", err)
		}
	}

	assertOnlyAccount := func(service *Service, expected string) {
		t.Helper()
		accounts, err := service.GetAccounts()
		if err != nil {
			t.Fatalf("GetAccounts(%s): %v", expected, err)
		}
		if len(accounts) != 1 || accounts[0] != expected {
			t.Fatalf("accounts = %v, want only %q", accounts, expected)
		}
		transactions, err := service.GetTransactions("", "")
		if err != nil {
			t.Fatalf("GetTransactions(%s): %v", expected, err)
		}
		if len(transactions) != 1 || transactions[0].Account != expected {
			t.Fatalf("transactions = %#v, want only account %q", transactions, expected)
		}
	}
	assertOnlyAccount(serviceA, "vault-a-sentinel")
	assertOnlyAccount(serviceB, "vault-b-sentinel")

	for name, instance := range map[string]*database.Instance{"a": instanceA, "b": instanceB} {
		var idempotency, quota int
		if err := instance.DB().QueryRow("SELECT COUNT(*) FROM ai_transaction_idempotency").Scan(&idempotency); err != nil {
			t.Fatalf("vault %s idempotency count: %v", name, err)
		}
		if err := instance.DB().QueryRow("SELECT COUNT(*) FROM ai_daily_transaction_usage").Scan(&quota); err != nil {
			t.Fatalf("vault %s quota count: %v", name, err)
		}
		if idempotency != 1 || quota != 1 {
			t.Fatalf("vault %s AI state = idempotency:%d quota:%d, want 1/1", name, idempotency, quota)
		}
	}
}

func TestServiceFailsClosedForNilAndClosedInstance(t *testing.T) {
	if service, err := NewService(nil); service != nil || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("NewService(nil) = %#v, %v; want nil, ErrServiceUnavailable", service, err)
	}
	// Keep the historical Desktop database live while testing the explicit
	// service. A closed explicit vault must not fall back to this sentinel.
	if err := database.InitDB(filepath.Join(t.TempDir(), "legacy-default.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	if _, err := database.GetDB().Exec(`INSERT INTO transactions
		(account, date, item, type, amount, balance, memo)
		VALUES ('legacy-sentinel', '2026-08-28', 'must-not-leak', 'expense', 1, -1, '')`); err != nil {
		t.Fatal(err)
	}

	closed, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), "closed-before-construction.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if service, err := NewService(closed); service != nil || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("NewService(closed) = %#v, %v; want nil, ErrServiceUnavailable", service, err)
	}

	instance, service := openCoreTestService(t, "closed-after-construction")
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAccounts(); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("GetAccounts after Close = %v, want ErrServiceUnavailable", err)
	}
	validIdentity := aiTransactionIdentity(
		"closed-service",
		"closed-key",
		"closed-request",
		1,
		time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
	)
	if _, err := service.AddAITransaction(context.Background(), models.TransactionRequest{
		Account: "must-not-fallback",
		Date:    "2026-08-28",
		Item:    "closed",
		Type:    "expense",
		Amount:  1,
	}, validIdentity); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("AddAITransaction after Close = %v, want ErrServiceUnavailable", err)
	}

	var nilService *Service
	if _, err := nilService.GetAccounts(); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("nil Service.GetAccounts = %v, want ErrServiceUnavailable", err)
	}
}

func TestGuardedServiceFailsClosedWhenOwnerRevokesAccess(t *testing.T) {
	instance, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), "guarded.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()

	available := true
	service, err := NewGuardedService(instance, func() bool { return available })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAccounts(); err != nil {
		t.Fatalf("live guarded service: %v", err)
	}
	available = false
	if _, err := service.GetAccounts(); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("revoked guarded service error = %v", err)
	}
	if service, err := NewGuardedService(instance, func() bool { return false }); service != nil || !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("NewGuardedService(revoked) = %#v, %v", service, err)
	}
}
