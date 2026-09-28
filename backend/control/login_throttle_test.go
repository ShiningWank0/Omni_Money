package control

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func testLoginThrottlePolicy() LoginThrottlePolicy {
	return LoginThrottlePolicy{
		MaxFailures: 5,
		BaseLock:    15 * time.Minute,
		MaxLock:     60 * time.Minute,
		Decay:       24 * time.Hour,
	}
}

func testLoginThrottleKey() string {
	return strings.Repeat("ab", 32)
}

func registerTestFailures(t *testing.T, store *Store, key string, now time.Time, count int) time.Time {
	t.Helper()
	var lockedUntil time.Time
	for index := 0; index < count; index++ {
		value, err := store.RegisterLoginFailure(context.Background(), key, now, testLoginThrottlePolicy())
		if err != nil {
			t.Fatalf("register login failure: %v", err)
		}
		lockedUntil = value
	}
	return lockedUntil
}

func TestLoginThrottleLocksAfterMaxFailures(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	if locked := registerTestFailures(t, store, key, testNow, 4); !locked.IsZero() {
		t.Fatalf("lock after four failures = %s, want none", locked)
	}
	locked := registerTestFailures(t, store, key, testNow, 1)
	want := testNow.Add(15 * time.Minute)
	if !locked.Equal(want) {
		t.Fatalf("lock after five failures = %s, want %s", locked, want)
	}
	status, err := store.LoginThrottleLockedUntil(context.Background(), key, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Equal(want) {
		t.Fatalf("reported lock = %s, want %s", status, want)
	}
	expired, err := store.LoginThrottleLockedUntil(context.Background(), key, want)
	if err != nil {
		t.Fatal(err)
	}
	if !expired.IsZero() {
		t.Fatalf("expired lock still reported: %s", expired)
	}
}

func TestLoginThrottleEscalatesAndCapsLockDuration(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	if locked := registerTestFailures(t, store, key, testNow, 5); !locked.Equal(testNow.Add(15 * time.Minute)) {
		t.Fatalf("first lock = %s", locked)
	}
	second := testNow.Add(16 * time.Minute)
	if locked := registerTestFailures(t, store, key, second, 5); !locked.Equal(second.Add(30 * time.Minute)) {
		t.Fatalf("second lock = %s", locked)
	}
	third := second.Add(31 * time.Minute)
	if locked := registerTestFailures(t, store, key, third, 5); !locked.Equal(third.Add(60 * time.Minute)) {
		t.Fatalf("third lock = %s", locked)
	}
	fourth := third.Add(61 * time.Minute)
	if locked := registerTestFailures(t, store, key, fourth, 5); !locked.Equal(fourth.Add(60 * time.Minute)) {
		t.Fatalf("capped lock = %s", locked)
	}
}

func TestLoginThrottleEscalationStaysWithinSchemaLimits(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	now := testNow
	for cycle := 0; cycle < 70; cycle++ {
		locked := registerTestFailures(t, store, key, now, 5)
		if locked.IsZero() {
			t.Fatalf("cycle %d produced no lock", cycle)
		}
		if cycle >= 2 && !locked.Equal(now.Add(60*time.Minute)) {
			t.Fatalf("cycle %d lock = %s, want capped 60m", cycle, locked)
		}
		now = locked.Add(time.Minute)
	}
}

func TestLoginThrottleDecayResetsProgression(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	if locked := registerTestFailures(t, store, key, testNow, 5); !locked.Equal(testNow.Add(15 * time.Minute)) {
		t.Fatalf("first lock = %s", locked)
	}
	later := testNow.Add(25 * time.Hour)
	if locked := registerTestFailures(t, store, key, later, 5); !locked.Equal(later.Add(15 * time.Minute)) {
		t.Fatalf("lock after decay = %s, want base lock", locked)
	}
}

func TestLoginThrottleLockCannotBeExtendedByTraffic(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	locked := registerTestFailures(t, store, key, testNow, 5)
	duringLock := testNow.Add(time.Minute)
	for attempt := 0; attempt < 3; attempt++ {
		got, err := store.RegisterLoginFailure(context.Background(), key, duringLock, testLoginThrottlePolicy())
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(locked) {
			t.Fatalf("lock moved during lock: %s, want %s", got, locked)
		}
	}
	nextCycle := registerTestFailures(t, store, key, locked, 5)
	if !nextCycle.Equal(locked.Add(30 * time.Minute)) {
		t.Fatalf("next cycle lock = %s, want %s", nextCycle, locked.Add(30*time.Minute))
	}
}

func TestLoginThrottleClearResetsCounter(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	registerTestFailures(t, store, key, testNow, 4)
	if err := store.ClearLoginFailures(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	status, err := store.LoginThrottleLockedUntil(context.Background(), key, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsZero() {
		t.Fatalf("cleared throttle still locked: %s", status)
	}
	if locked := registerTestFailures(t, store, key, testNow, 4); !locked.IsZero() {
		t.Fatalf("counter survived clear: %s", locked)
	}
}

func TestLoginThrottleConcurrentFailuresAreAtomic(t *testing.T) {
	store := openTestStore(t)
	key := testLoginThrottleKey()
	policy := testLoginThrottlePolicy()
	policy.MaxFailures = 1000
	var group sync.WaitGroup
	for index := 0; index < 100; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.RegisterLoginFailure(context.Background(), key, testNow, policy); err != nil {
				t.Errorf("concurrent register: %v", err)
			}
		}()
	}
	group.Wait()
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT failure_count FROM login_throttle WHERE account_key = ?`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("atomic failure count = %d, want 100", count)
	}
}

func TestLoginThrottlePurgesDecayedRows(t *testing.T) {
	store := openTestStore(t)
	staleKey := strings.Repeat("cd", 32)
	registerTestFailures(t, store, staleKey, testNow, 1)
	later := testNow.Add(48 * time.Hour)
	for index := 0; index < loginThrottleGCInterval; index++ {
		key := fmt.Sprintf("%062x%02x", index, index)
		if _, err := store.RegisterLoginFailure(context.Background(), key, later, testLoginThrottlePolicy()); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM login_throttle WHERE account_key = ?`, staleKey).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("decayed throttle row was not purged: %d", remaining)
	}
}

func TestLoginThrottleRejectsInvalidInput(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.LoginThrottleLockedUntil(context.Background(), "short", testNow); err == nil {
		t.Fatal("short throttle key was accepted")
	}
	if _, err := store.RegisterLoginFailure(context.Background(), strings.Repeat("AB", 32), testNow, testLoginThrottlePolicy()); err == nil {
		t.Fatal("uppercase throttle key was accepted")
	}
	if _, err := store.RegisterLoginFailure(context.Background(), testLoginThrottleKey(), testNow, LoginThrottlePolicy{}); err == nil {
		t.Fatal("empty throttle policy was accepted")
	}
	if err := store.ClearLoginFailures(context.Background(), ""); err == nil {
		t.Fatal("empty throttle key was accepted")
	}
}
