package vault

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/securedb"
)

const testVaultID = "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M"
const secondTestVaultID = "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N"

func testKey(fill byte) securedb.RawKey {
	var key securedb.RawKey
	for index := range key {
		key[index] = fill
	}
	return key
}

func newPlainTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := newManager(filepath.Join(t.TempDir(), "vaults"), func(path string, _ securedb.RawKey) (*database.Instance, error) {
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// Tests in this package may inspect the manager-owned instance directly. The
// production Lease API intentionally exposes only a guarded core.Service.
func leaseInstance(lease *Lease) *database.Instance {
	if lease == nil {
		return nil
	}
	if lease.state == nil {
		return nil
	}
	lease.state.mu.RLock()
	defer lease.state.mu.RUnlock()
	if lease.state.released || lease.entry == nil {
		return nil
	}
	return lease.entry.instance
}

func leaseDB(lease *Lease) *sql.DB {
	instance := leaseInstance(lease)
	if instance == nil {
		return nil
	}
	return instance.DB()
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func TestAcquireReusesOnlyExactBinding(t *testing.T) {
	manager := newPlainTestManager(t)
	first, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	if leaseInstance(first) != leaseInstance(second) || leaseDB(first) == nil {
		t.Fatal("exact binding did not reuse the open instance")
	}
	if _, err := manager.Acquire("user-1", "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4N", testKey(0x11)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("vault mismatch error = %v", err)
	}
	if _, err := manager.Acquire("user-1", testVaultID, testKey(0x12)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("key mismatch error = %v", err)
	}
	first.Release()
	second.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsVaultSharedAcrossUsers(t *testing.T) {
	manager := newPlainTestManager(t)
	first, err := manager.Acquire("user-1", testVaultID, testKey(0x11))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		first.Release()
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x11)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("shared vault with same key error = %v", err)
	}
	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x12)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("shared vault with different key error = %v", err)
	}
}

func TestCloseUserWaitsForLeaseAndRejectsNewLease(t *testing.T) {
	manager := newPlainTestManager(t)
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x21))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()

	deadline := time.Now().Add(time.Second)
	for {
		var probe *Lease
		probe, err = manager.Acquire("user-1", testVaultID, testKey(0x21))
		if errors.Is(err, ErrDraining) {
			break
		}
		if err == nil {
			probe.Release()
			time.Sleep(time.Millisecond)
			continue
		}
		if time.Now().After(deadline) {
			t.Fatalf("vault did not enter draining state: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatalf("CloseUser returned before release: %v", err)
	default:
	}
	lease.Release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if leaseDB(lease) != nil || leaseInstance(lease) != nil {
		t.Fatal("released lease retained database access")
	}
}

func TestBeginUserDrainRejectsNewLeasesAndWaitsForExistingChild(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x26))
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}

	wait, err := manager.BeginUserDrain("user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire("user-1", testVaultID, testKey(0x26)); !errors.Is(err, ErrDraining) {
		t.Fatalf("Acquire after BeginUserDrain error = %v", err)
	}
	if _, err := root.Borrow(); !errors.Is(err, ErrDraining) {
		t.Fatalf("Borrow after BeginUserDrain error = %v", err)
	}

	drained := make(chan error, 1)
	go func() { drained <- wait(context.Background()) }()
	root.Release()
	select {
	case err := <-drained:
		t.Fatalf("drain waiter returned while an existing child was live: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if leaseDB(child) == nil {
		t.Fatal("BeginUserDrain revoked an already-borrowed child")
	}
	child.Release()
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
}

func TestBeginUserDrainWaiterStaysBoundToObservedEntry(t *testing.T) {
	manager := newPlainTestManager(t)
	oldRoot, err := manager.Acquire("user-1", testVaultID, testKey(0x27))
	if err != nil {
		t.Fatal(err)
	}
	staleWaiter, err := manager.BeginUserDrain("user-1")
	if err != nil {
		t.Fatal(err)
	}
	closingWaiter, err := manager.BeginUserDrain("user-1")
	if err != nil {
		t.Fatal(err)
	}
	oldRoot.Release()
	if err := closingWaiter(context.Background()); err != nil {
		t.Fatal(err)
	}

	newRoot, err := manager.Acquire("user-1", secondTestVaultID, testKey(0x28))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		newRoot.Release()
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := staleWaiter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if leaseDB(newRoot) == nil {
		t.Fatal("waiter for the old entry closed the replacement entry")
	}
	child, err := newRoot.Borrow()
	if err != nil {
		t.Fatalf("replacement entry was left draining: %v", err)
	}
	child.Release()
}

func TestBeginUserDrainWithoutEntryReturnsNoOpWaiter(t *testing.T) {
	manager := newPlainTestManager(t)
	wait, err := manager.BeginUserDrain("user-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx); err != nil {
		t.Fatalf("no-op drain waiter error = %v", err)
	}
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x29))
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledDrainWaiterStillClosesOnFinalChildRelease(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x2a))
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	oldEntry := child.entry
	oldInstance := leaseInstance(child)
	if oldEntry == nil || oldInstance == nil {
		t.Fatal("initial vault entry was not open")
	}
	wait, err := manager.BeginUserDrain("user-1")
	if err != nil {
		t.Fatal(err)
	}
	root.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled drain waiter error = %v", err)
	}
	if oldInstance.DB() == nil {
		t.Fatal("canceled drain waiter closed an entry with a live child")
	}

	child.Release()
	if oldInstance.DB() != nil {
		t.Fatal("final child Release returned before closing the drained instance")
	}
	manager.mu.Lock()
	entryStillMapped := manager.entries["user-1"] == oldEntry || manager.byVault[testVaultID] == oldEntry
	fingerprintCleared := true
	for _, value := range oldEntry.keyFingerprint {
		fingerprintCleared = fingerprintCleared && value == 0
	}
	manager.mu.Unlock()
	if entryStillMapped || !fingerprintCleared {
		t.Fatalf("final child Release did not remove and zeroize old entry: mapped=%t zeroized=%t", entryStillMapped, fingerprintCleared)
	}

	reopened, err := manager.Acquire("user-1", testVaultID, testKey(0x2a))
	if err != nil {
		t.Fatalf("reacquire after canceled drain: %v", err)
	}
	if leaseInstance(reopened) == oldInstance || leaseDB(reopened) == nil {
		t.Fatal("reacquire did not open a fresh vault instance")
	}
	reopened.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBorrowKeepsVaultAliveAfterRootRelease(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x22))
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Borrow(); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("Borrow from child error = %v", err)
	}
	instance := leaseInstance(child)
	if instance == nil || leaseDB(child) == nil {
		t.Fatal("borrowed request lease has no database")
	}
	service, err := child.Service()
	if err != nil {
		t.Fatal(err)
	}

	root.Release()
	root.Release()
	if _, err := root.Borrow(); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("Borrow after root Release error = %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()
	select {
	case err := <-closed:
		t.Fatalf("CloseUser returned while child was live: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if leaseDB(child) == nil {
		t.Fatal("root Release revoked an existing child")
	}
	child.Release()
	child.Release()
	if _, err := service.GetAccounts(); !errors.Is(err, core.ErrServiceUnavailable) {
		t.Fatalf("service retained access after child release: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if leaseDB(child) != nil || instance.DB() != nil {
		t.Fatal("child release did not allow the instance to close")
	}
}

func TestLastRootReleaseAutomaticallyClosesAfterRequestChildren(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "vaults")
	opened := make(chan *database.Instance, 1)
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		instance, openErr := database.OpenPlainInstance(path)
		if openErr == nil {
			opened <- instance
		}
		return instance, openErr
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())

	root, err := manager.Acquire("user-1", testVaultID, testKey(0x24))
	if err != nil {
		t.Fatal(err)
	}
	instance := <-opened
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	service, err := child.Service()
	if err != nil {
		t.Fatal(err)
	}

	root.Release()
	if _, err := root.Borrow(); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("Borrow after final root release error = %v", err)
	}
	if _, err := service.GetAccounts(); err != nil {
		t.Fatalf("live request child was revoked by root release: %v", err)
	}
	child.Release()

	deadline := time.Now().Add(2 * time.Second)
	for instance.DB() != nil {
		if time.Now().After(deadline) {
			t.Fatal("vault remained open after its final root and child were released")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.GetAccounts(); !errors.Is(err, core.ErrServiceUnavailable) {
		t.Fatalf("released request service retained database access: %v", err)
	}
}

func TestServiceCreationRacingFinalRootReleaseIsSafe(t *testing.T) {
	for iteration := range 100 {
		manager := newPlainTestManager(t)
		root, err := manager.Acquire("user-1", testVaultID, testKey(byte(iteration+1)))
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		created := make(chan error, 1)
		go func() {
			<-start
			service, serviceErr := root.Service()
			if serviceErr == nil {
				_, serviceErr = service.GetAccounts()
			}
			created <- serviceErr
		}()
		close(start)
		root.Release()
		serviceErr := <-created
		if serviceErr != nil && !errors.Is(serviceErr, ErrLeaseReleased) && !errors.Is(serviceErr, core.ErrServiceUnavailable) {
			t.Fatalf("iteration %d: Service race error = %v", iteration, serviceErr)
		}
		if err := manager.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopiedLeaseSharesExactlyOnceReleaseState(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x25))
	if err != nil {
		t.Fatal(err)
	}
	child, err := root.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	service, err := child.Service()
	if err != nil {
		t.Fatal(err)
	}
	copyOfRoot := *root
	root.Release()
	copyOfRoot.Release()

	manager.mu.Lock()
	current := manager.entries["user-1"]
	references := 0
	rootReferences := 0
	if current != nil {
		references = current.references
		rootReferences = current.rootReferences
	}
	manager.mu.Unlock()
	if references != 1 || rootReferences != 0 {
		t.Fatalf("copied root double-released references: refs=%d roots=%d", references, rootReferences)
	}
	if _, err := service.GetAccounts(); err != nil {
		t.Fatalf("copied root release revoked the live child: %v", err)
	}
	child.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBorrowRacingWithCloseUserFailsClosed(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x23))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		child, borrowErr := root.Borrow()
		if errors.Is(borrowErr, ErrDraining) {
			break
		}
		if borrowErr != nil {
			t.Fatalf("Borrow error = %v", borrowErr)
		}
		child.Release()
		if time.Now().After(deadline) {
			t.Fatal("CloseUser did not stop new child leases")
		}
	}
	root.Release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestCloseTimeoutFailsClosed(t *testing.T) {
	manager := newPlainTestManager(t)
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x31))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x31)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Acquire after timed-out Close error = %v", err)
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVaultIdentifierCannotEscapeRoot(t *testing.T) {
	manager := newPlainTestManager(t)
	for _, value := range []string{"../outside-vault-id", "vault/with/slash-123", "short"} {
		if _, err := manager.Acquire("user-1", value, testKey(0x41)); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("Acquire(%q) error = %v", value, err)
		}
	}
	want := filepath.Join(manager.root, testVaultID, "ledger.db")
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x41))
	if err != nil {
		t.Fatal(err)
	}
	if leaseInstance(lease) == nil {
		t.Fatal("valid vault did not open")
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(want)) != manager.root {
		t.Fatal("vault path escaped manager root")
	}
}

func TestAcquireSameUserWaitsForOneOpeningReservation(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "vaults")
	started := make(chan struct{}, 2)
	allowOpen := make(chan struct{})
	var openCalls atomic.Int32
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		openCalls.Add(1)
		started <- struct{}{}
		<-allowOpen
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		lease *Lease
		err   error
	}
	results := make(chan result, 2)
	go func() {
		lease, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x51))
		results <- result{lease: lease, err: acquireErr}
	}()
	waitForSignal(t, started, "first open did not start")
	go func() {
		lease, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x51))
		results <- result{lease: lease, err: acquireErr}
	}()
	select {
	case <-started:
		t.Fatal("same-user waiter started a duplicate open")
	case <-time.After(25 * time.Millisecond):
	}
	close(allowOpen)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("Acquire errors = %v, %v", first.err, second.err)
	}
	if openCalls.Load() != 1 || leaseInstance(first.lease) != leaseInstance(second.lease) {
		t.Fatalf("open calls = %d; same binding did not share instance", openCalls.Load())
	}
	first.lease.Release()
	second.lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingAcquireCannotReopenAfterCloseUser(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "vaults")
	started := make(chan struct{})
	allowOpen := make(chan struct{})
	var openCalls atomic.Int32
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		openCalls.Add(1)
		if openCalls.Load() == 1 {
			close(started)
			<-allowOpen
		}
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x52))
		firstResult <- acquireErr
	}()
	waitForSignal(t, started, "first open did not start")

	waitingResult := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x52))
		waitingResult <- acquireErr
	}()
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		draining := manager.entries["user-1"] != nil && manager.entries["user-1"].draining
		manager.mu.Unlock()
		if draining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CloseUser did not mark the opening reservation as draining")
		}
		time.Sleep(time.Millisecond)
	}
	close(allowOpen)

	if err := <-firstResult; !errors.Is(err, ErrDraining) {
		t.Fatalf("opening Acquire error = %v, want ErrDraining", err)
	}
	if err := <-waitingResult; !errors.Is(err, ErrDraining) {
		t.Fatalf("waiting Acquire error = %v, want ErrDraining", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("waiting Acquire reopened the vault %d times", openCalls.Load())
	}
}

func TestAcquireDifferentUsersOpensInParallel(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "vaults")
	started := make(chan struct{}, 2)
	allowOpen := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-allowOpen
		active.Add(-1)
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}

	leases := make(chan *Lease, 2)
	errorsCh := make(chan error, 2)
	go func() {
		lease, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x61))
		leases <- lease
		errorsCh <- acquireErr
	}()
	go func() {
		lease, acquireErr := manager.Acquire("user-2", secondTestVaultID, testKey(0x62))
		leases <- lease
		errorsCh <- acquireErr
	}()
	waitForSignal(t, started, "first parallel open did not start")
	waitForSignal(t, started, "second user was blocked by the manager mutex")
	if maximum.Load() < 2 {
		t.Fatalf("maximum concurrent opens = %d, want at least 2", maximum.Load())
	}
	close(allowOpen)
	var acquired []*Lease
	for range 2 {
		acquired = append(acquired, <-leases)
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	for _, lease := range acquired {
		lease.Release()
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCrossUserCollisionRejectedWhileVaultIsOpening(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "vaults")
	started := make(chan struct{})
	allowOpen := make(chan struct{})
	var openCalls atomic.Int32
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		openCalls.Add(1)
		close(started)
		<-allowOpen
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	var first *Lease
	go func() {
		var acquireErr error
		first, acquireErr = manager.Acquire("user-1", testVaultID, testKey(0x71))
		firstResult <- acquireErr
	}()
	waitForSignal(t, started, "reserved vault did not start opening")
	if _, err := manager.Acquire("user-2", testVaultID, testKey(0x71)); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("cross-user opening collision error = %v", err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("cross-user collision started %d opens", openCalls.Load())
	}
	close(allowOpen)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	first.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseAndCloseUserTimeoutDuringOpenRemainFailClosed(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		name := "close-user"
		if closeManager {
			name = "close-manager"
		}
		t.Run(name, func(t *testing.T) {
			rootPath := filepath.Join(t.TempDir(), "vaults")
			started := make(chan struct{})
			allowOpen := make(chan struct{})
			opened := make(chan *database.Instance, 1)
			manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
				close(started)
				<-allowOpen
				instance, openErr := database.OpenPlainInstance(path)
				if openErr == nil {
					opened <- instance
				}
				return instance, openErr
			})
			if err != nil {
				t.Fatal(err)
			}
			acquired := make(chan error, 1)
			go func() {
				_, acquireErr := manager.Acquire("user-1", testVaultID, testKey(0x72))
				acquired <- acquireErr
			}()
			waitForSignal(t, started, "open did not start")

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if closeManager {
				err = manager.Close(ctx)
			} else {
				err = manager.CloseUser(ctx, "user-1")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timed close error = %v", err)
			}
			close(allowOpen)
			acquireErr := <-acquired
			if closeManager && !errors.Is(acquireErr, ErrClosed) {
				t.Fatalf("Acquire after Close timeout error = %v", acquireErr)
			}
			if !closeManager && !errors.Is(acquireErr, ErrDraining) {
				t.Fatalf("Acquire after CloseUser timeout error = %v", acquireErr)
			}
			instance := <-opened
			if instance.DB() != nil {
				t.Fatal("late-opened instance remained open after a timed-out close")
			}
			if closeManager {
				err = manager.Close(context.Background())
			} else {
				err = manager.CloseUser(context.Background(), "user-1")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVaultDirectoriesArePrivateAndRejectUnsafePaths(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "vaults")
	manager, err := newManager(rootPath, func(path string, _ securedb.RawKey) (*database.Instance, error) {
		return database.OpenPlainInstance(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0700 {
		t.Fatalf("vault root mode = %04o", rootInfo.Mode().Perm())
	}
	lease, err := manager.Acquire("user-1", testVaultID, testKey(0x81))
	if err != nil {
		t.Fatal(err)
	}
	vaultDir := filepath.Join(rootPath, testVaultID)
	vaultInfo, err := os.Lstat(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	if vaultInfo.Mode().Perm() != 0700 {
		t.Fatalf("per-vault directory mode = %04o", vaultInfo.Mode().Perm())
	}
	lease.Release()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Run("root symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "vault-link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := newManager(link, func(string, securedb.RawKey) (*database.Instance, error) { return nil, nil }); err == nil {
			t.Fatal("symlink vault root was accepted")
		}
	})

	t.Run("insecure root", func(t *testing.T) {
		insecure := filepath.Join(t.TempDir(), "insecure-root")
		if err := os.Mkdir(insecure, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(insecure, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := newManager(insecure, func(string, securedb.RawKey) (*database.Instance, error) { return nil, nil }); err == nil {
			t.Fatal("insecure vault root was accepted")
		}
	})

	t.Run("root regular file", func(t *testing.T) {
		rootFile := filepath.Join(t.TempDir(), "vault-file")
		if err := os.WriteFile(rootFile, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newManager(rootFile, func(string, securedb.RawKey) (*database.Instance, error) { return nil, nil }); err == nil {
			t.Fatal("regular-file vault root was accepted")
		}
	})

	for name, prepare := range map[string]func(string) error{
		"vault symlink": func(path string) error {
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				return err
			}
			return os.Symlink(outside, path)
		},
		"vault regular file": func(path string) error {
			return os.WriteFile(path, []byte("not a directory"), 0600)
		},
		"insecure vault directory": func(path string) error {
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			return os.Chmod(path, 0755)
		},
		"ledger symlink": func(path string) error {
			if err := os.Mkdir(path, 0700); err != nil {
				return err
			}
			target := filepath.Join(t.TempDir(), "outside-ledger.db")
			if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
				return err
			}
			return os.Symlink(target, filepath.Join(path, "ledger.db"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			unsafeRoot := filepath.Join(t.TempDir(), "vaults")
			unsafeManager, err := newManager(unsafeRoot, func(path string, _ securedb.RawKey) (*database.Instance, error) {
				return database.OpenPlainInstance(path)
			})
			if err != nil {
				t.Fatal(err)
			}
			vaultPath := filepath.Join(unsafeRoot, testVaultID)
			if err := prepare(vaultPath); err != nil {
				t.Skipf("unsafe path fixture unavailable: %v", err)
			}
			if _, err := unsafeManager.Acquire("user-1", testVaultID, testKey(0x82)); err == nil {
				t.Fatal("unsafe per-vault path was accepted")
			}
		})
	}
}

func TestConcurrentBorrowReleaseAndClose(t *testing.T) {
	manager := newPlainTestManager(t)
	root, err := manager.Acquire("user-1", testVaultID, testKey(0x91))
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				child, borrowErr := root.Borrow()
				if borrowErr != nil {
					if errors.Is(borrowErr, ErrDraining) || errors.Is(borrowErr, ErrLeaseReleased) {
						return
					}
					t.Errorf("Borrow error = %v", borrowErr)
					return
				}
				if leaseDB(child) == nil {
					t.Error("borrowed child lost database access")
				}
				child.Release()
				child.Release()
			}
		}()
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseUser(context.Background(), "user-1") }()
	root.Release()
	workers.Wait()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
