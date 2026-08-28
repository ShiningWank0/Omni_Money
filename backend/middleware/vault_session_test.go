package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/core"
	"omni_money/backend/database"
	"omni_money/backend/vault"
)

const testVaultSessionUserID = "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M"

func testControlUser(id string) control.UserSummary {
	return control.UserSummary{
		ID:          id,
		Email:       id + "@example.test",
		DisplayName: id,
		Role:        control.RoleUser,
		State:       control.UserActive,
	}
}

func countingSessionRoot(userID string, released *atomic.Int32) *sessionVaultRoot {
	return &sessionVaultRoot{
		userID: userID,
		borrow: func() (*requestVaultLease, error) {
			return nil, errors.New("borrow is not expected in this test")
		},
		release: func() { released.Add(1) },
	}
}

func createTestVaultSession(t *testing.T, manager *SessionManager, user control.UserSummary, root *sessionVaultRoot) *Session {
	t.Helper()
	session, err := manager.createSession(user.Email, &user, root)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestVaultSessionRootReleasedExactlyOnceOnEveryRemovalPath(t *testing.T) {
	start := time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC)

	t.Run("delete and delete all", func(t *testing.T) {
		config := securityTestSessionConfig()
		config.MaxConcurrent = 4
		manager, _ := newClockedSessionManager(t, config, start)
		user := testControlUser(testVaultSessionUserID)
		var firstReleased, secondReleased, thirdReleased atomic.Int32
		first := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &firstReleased))
		createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &secondReleased))
		createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &thirdReleased))

		manager.DeleteSession(first.ID)
		manager.DeleteSession(first.ID)
		if got := firstReleased.Load(); got != 1 {
			t.Fatalf("DeleteSession releases = %d, want 1", got)
		}
		if deleted := manager.DeleteAllSessions(user.Email); deleted != 2 {
			t.Fatalf("DeleteAllSessions deleted = %d, want 2", deleted)
		}
		if got := secondReleased.Load(); got != 1 {
			t.Fatalf("DeleteAllSessions releases second = %d, want 1", got)
		}
		if got := thirdReleased.Load(); got != 1 {
			t.Fatalf("DeleteAllSessions releases third = %d, want 1", got)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		manager, clock := newClockedSessionManager(t, securityTestSessionConfig(), start)
		user := testControlUser(testVaultSessionUserID)
		var released atomic.Int32
		session := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &released))
		*clock = start.Add(manager.config.IdleTimeout)
		if _, ok := manager.GetSession(session.ID); ok {
			t.Fatal("expired vault session remained valid")
		}
		if got := released.Load(); got != 1 {
			t.Fatalf("expiry releases = %d, want 1", got)
		}
	})

	t.Run("oldest eviction", func(t *testing.T) {
		config := securityTestSessionConfig()
		config.MaxConcurrent = 1
		manager, clock := newClockedSessionManager(t, config, start)
		user := testControlUser(testVaultSessionUserID)
		var oldestReleased, newestReleased atomic.Int32
		oldest := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &oldestReleased))
		*clock = start.Add(time.Second)
		newest := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &newestReleased))
		if _, ok := manager.GetSession(oldest.ID); ok {
			t.Fatal("oldest vault session was not evicted")
		}
		if got := oldestReleased.Load(); got != 1 {
			t.Fatalf("eviction releases = %d, want 1", got)
		}
		if _, ok := manager.GetSession(newest.ID); !ok {
			t.Fatal("newest vault session was evicted")
		}
		manager.Close()
		if got := newestReleased.Load(); got != 1 {
			t.Fatalf("Close releases newest = %d, want 1", got)
		}
	})

	t.Run("close", func(t *testing.T) {
		manager := NewSessionManagerWithConfig(securityTestSessionConfig())
		user := testControlUser(testVaultSessionUserID)
		var released atomic.Int32
		createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &released))
		manager.Close()
		manager.Close()
		if got := released.Load(); got != 1 {
			t.Fatalf("Close releases = %d, want 1", got)
		}
	})
}

func TestVaultSessionRotationTransfersRootWithoutRelease(t *testing.T) {
	manager, _ := newClockedSessionManager(t, securityTestSessionConfig(), time.Now())
	user := testControlUser(testVaultSessionUserID)
	var released atomic.Int32
	session := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &released))

	rotated, err := manager.RotateAfterReauthentication(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := released.Load(); got != 0 {
		t.Fatalf("rotation released root %d times", got)
	}
	manager.DeleteSession(session.ID)
	if got := released.Load(); got != 0 {
		t.Fatalf("deleting old rotated ID released root %d times", got)
	}
	manager.DeleteSession(rotated.ID)
	manager.DeleteSession(rotated.ID)
	if got := released.Load(); got != 1 {
		t.Fatalf("deleting rotated session released root %d times, want 1", got)
	}
}

func TestVaultSessionCreationFailureLeavesRootWithCaller(t *testing.T) {
	manager := NewSessionManagerWithConfig(securityTestSessionConfig())
	manager.Close()
	user := testControlUser(testVaultSessionUserID)
	var released atomic.Int32
	root := countingSessionRoot(user.ID, &released)

	if session, err := manager.createSession(user.Email, &user, root); session != nil || !errors.Is(err, ErrSessionManagerClosed) {
		t.Fatalf("createSession after Close = %#v, %v", session, err)
	}
	if got := released.Load(); got != 0 {
		t.Fatalf("failed creation stole root ownership and released %d times", got)
	}
	root.Release()
	root.Release()
	if got := released.Load(); got != 1 {
		t.Fatalf("caller root release count = %d, want 1", got)
	}
}

func TestSlowVaultRootReleaseDoesNotHoldSessionManagerMutex(t *testing.T) {
	manager := NewSessionManagerWithConfig(securityTestSessionConfig())
	releaseStarted := make(chan struct{})
	allowRelease := make(chan struct{})
	var allowOnce sync.Once
	unblockRelease := func() { allowOnce.Do(func() { close(allowRelease) }) }
	t.Cleanup(func() {
		unblockRelease()
		manager.Close()
	})

	user := testControlUser(testVaultSessionUserID)
	blockingRoot := &sessionVaultRoot{
		userID: user.ID,
		release: func() {
			close(releaseStarted)
			<-allowRelease
		},
	}
	blocked := createTestVaultSession(t, manager, user, blockingRoot)
	other, err := manager.CreateSession("independent-session")
	if err != nil {
		t.Fatal(err)
	}

	deleteDone := make(chan struct{})
	go func() {
		manager.DeleteSession(blocked.ID)
		close(deleteDone)
	}()
	select {
	case <-releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("vault root release did not start")
	}

	lookupDone := make(chan bool, 1)
	go func() {
		_, ok := manager.GetSession(other.ID)
		lookupDone <- ok
	}()
	select {
	case ok := <-lookupDone:
		if !ok {
			t.Fatal("independent session disappeared during another root release")
		}
	case <-time.After(time.Second):
		t.Fatal("slow vault root release held the global session mutex")
	}

	unblockRelease()
	select {
	case <-deleteDone:
	case <-time.After(time.Second):
		t.Fatal("DeleteSession did not finish after root release unblocked")
	}
}

type fakeCurrentUserStore struct {
	mu    sync.Mutex
	user  control.UserSummary
	err   error
	calls int
}

func (store *fakeCurrentUserStore) GetUser(_ context.Context, _ string) (control.UserSummary, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls++
	return store.user, store.err
}

func (store *fakeCurrentUserStore) setUser(user control.UserSummary) {
	store.mu.Lock()
	store.user = user
	store.mu.Unlock()
}

type fakeVaultLifecycle struct {
	mu              sync.Mutex
	cond            *sync.Cond
	service         *core.Service
	userID          string
	references      int
	draining        bool
	rootReleases    int
	childBorrows    int
	childReleases   int
	duplicateErrors int
}

func newFakeVaultLifecycle(t *testing.T, instance *database.Instance, userID string) *fakeVaultLifecycle {
	t.Helper()
	service, err := core.NewService(instance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &fakeVaultLifecycle{service: service, userID: userID, references: 1}
	lifecycle.cond = sync.NewCond(&lifecycle.mu)
	return lifecycle
}

func (lifecycle *fakeVaultLifecycle) root() *sessionVaultRoot {
	root := &sessionVaultRoot{userID: lifecycle.userID}
	root.borrow = func() (*requestVaultLease, error) {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		if lifecycle.draining || lifecycle.references == 0 {
			return nil, vault.ErrDraining
		}
		lifecycle.references++
		lifecycle.childBorrows++
		return &requestVaultLease{
			service: lifecycle.service,
			release: lifecycle.releaseChild,
		}, nil
	}
	root.release = lifecycle.releaseRoot
	return root
}

func (lifecycle *fakeVaultLifecycle) releaseRoot() {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.references <= 0 || lifecycle.rootReleases != 0 {
		lifecycle.duplicateErrors++
		return
	}
	lifecycle.references--
	lifecycle.rootReleases++
	lifecycle.cond.Broadcast()
}

func (lifecycle *fakeVaultLifecycle) releaseChild() {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.references <= 0 || lifecycle.childReleases >= lifecycle.childBorrows {
		lifecycle.duplicateErrors++
		return
	}
	lifecycle.references--
	lifecycle.childReleases++
	lifecycle.cond.Broadcast()
}

func (lifecycle *fakeVaultLifecycle) closeUser() {
	lifecycle.mu.Lock()
	lifecycle.draining = true
	for lifecycle.references != 0 {
		lifecycle.cond.Wait()
	}
	lifecycle.mu.Unlock()
}

func (lifecycle *fakeVaultLifecycle) counts() (references, roots, borrows, children, duplicates int) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.references, lifecycle.rootReleases, lifecycle.childBorrows, lifecycle.childReleases, lifecycle.duplicateErrors
}

func openMiddlewareTestInstance(t *testing.T) *database.Instance {
	t.Helper()
	instance, err := database.OpenPlainInstance(filepath.Join(t.TempDir(), "request-vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Errorf("Close request vault: %v", err)
		}
	})
	return instance
}

func vaultSessionRequest(t *testing.T, manager *SessionManager, session *Session) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/accounts", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: session.ID})
	return request
}

func TestVaultSessionMiddlewareRefreshesUserAndReleasesChildAfterPanic(t *testing.T) {
	instance := openMiddlewareTestInstance(t)
	manager := NewSessionManagerWithConfig(securityTestSessionConfig())
	t.Cleanup(manager.Close)
	user := testControlUser(testVaultSessionUserID)
	lifecycle := newFakeVaultLifecycle(t, instance, user.ID)
	session := createTestVaultSession(t, manager, user, lifecycle.root())
	current := user
	current.Role = control.RoleAdmin
	store := &fakeCurrentUserStore{user: current}

	panicValue := errors.New("handler panic")
	handler := VaultSessionAuthMiddleware(manager, store, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotSession, ok := SessionFromContext(r.Context())
		if !ok || gotSession.UserID != user.ID || gotSession.Role != control.RoleAdmin {
			t.Fatalf("session context = %#v, %v", gotSession, ok)
		}
		gotUser, ok := AuthenticatedUserFromContext(r.Context())
		if !ok || gotUser.Role != control.RoleAdmin {
			t.Fatalf("current user context = %#v, %v", gotUser, ok)
		}
		service, ok := CoreServiceFromContext(r.Context())
		if !ok {
			t.Fatal("core service missing from request context")
		}
		if _, err := service.GetAccounts(); err != nil {
			t.Fatalf("request service used wrong instance: %v", err)
		}
		panic(panicValue)
	}))

	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("recovered panic = %#v, want %#v", recovered, panicValue)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), vaultSessionRequest(t, manager, session))
	}()

	references, roots, borrows, children, duplicates := lifecycle.counts()
	if references != 1 || roots != 0 || borrows != 1 || children != 1 || duplicates != 0 {
		t.Fatalf("post-panic lifecycle refs=%d roots=%d borrows=%d children=%d duplicates=%d", references, roots, borrows, children, duplicates)
	}
	manager.DeleteSession(session.ID)
	references, roots, _, _, duplicates = lifecycle.counts()
	if references != 0 || roots != 1 || duplicates != 0 {
		t.Fatalf("post-delete lifecycle refs=%d roots=%d duplicates=%d", references, roots, duplicates)
	}
}

func TestVaultSessionLongRequestSurvivesDisableWhileNewRequestsFailClosed(t *testing.T) {
	instance := openMiddlewareTestInstance(t)
	manager := NewSessionManagerWithConfig(securityTestSessionConfig())
	t.Cleanup(manager.Close)
	user := testControlUser(testVaultSessionUserID)
	lifecycle := newFakeVaultLifecycle(t, instance, user.ID)
	session := createTestVaultSession(t, manager, user, lifecycle.root())
	store := &fakeCurrentUserStore{user: user}
	entered := make(chan struct{})
	allowReturn := make(chan struct{})
	handler := VaultSessionAuthMiddleware(manager, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CoreServiceFromContext(r.Context()); !ok {
			t.Error("long request has no core service")
		}
		close(entered)
		<-allowReturn
		w.WriteHeader(http.StatusNoContent)
	}))

	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, vaultSessionRequest(t, manager, session))
		firstResponse <- recorder
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("long request did not enter handler")
	}

	disabled := user
	disabled.State = control.UserDisabled
	store.setUser(disabled)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, vaultSessionRequest(t, manager, session))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("disabled-user request status = %d, want 401", second.Code)
	}

	closed := make(chan struct{})
	go func() {
		lifecycle.closeUser()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("vault close completed while long request child was live")
	case <-time.After(25 * time.Millisecond):
	}

	third := httptest.NewRecorder()
	handler.ServeHTTP(third, vaultSessionRequest(t, manager, session))
	if third.Code != http.StatusUnauthorized {
		t.Fatalf("request after disable status = %d, want 401", third.Code)
	}
	close(allowReturn)
	select {
	case response := <-firstResponse:
		if response.Code != http.StatusNoContent {
			t.Fatalf("long request status = %d, want 204", response.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long request did not finish")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("vault close did not finish after child release")
	}

	references, roots, borrows, children, duplicates := lifecycle.counts()
	if references != 0 || roots != 1 || borrows != 2 || children != 2 || duplicates != 0 {
		t.Fatalf("final lifecycle refs=%d roots=%d borrows=%d children=%d duplicates=%d", references, roots, borrows, children, duplicates)
	}
}

func TestVaultSessionLongRequestSurvivesEverySessionRemovalPath(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(*testing.T, *SessionManager, *time.Time, control.UserSummary, *Session)
	}{
		{
			name: "logout",
			trigger: func(_ *testing.T, manager *SessionManager, _ *time.Time, _ control.UserSummary, session *Session) {
				manager.DeleteSession(session.ID)
			},
		},
		{
			name: "expiry",
			trigger: func(t *testing.T, manager *SessionManager, clock *time.Time, _ control.UserSummary, session *Session) {
				*clock = clock.Add(manager.config.IdleTimeout)
				if _, ok := manager.GetSession(session.ID); ok {
					t.Fatal("expired session survived")
				}
			},
		},
		{
			name: "oldest eviction",
			trigger: func(t *testing.T, manager *SessionManager, clock *time.Time, user control.UserSummary, session *Session) {
				*clock = clock.Add(time.Second)
				var replacementReleased atomic.Int32
				replacement := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &replacementReleased))
				if _, ok := manager.GetSession(session.ID); ok {
					t.Fatal("evicted session survived")
				}
				manager.DeleteSession(replacement.ID)
				if got := replacementReleased.Load(); got != 1 {
					t.Fatalf("replacement release count = %d, want 1", got)
				}
			},
		},
		{
			name: "session manager close before vault manager close",
			trigger: func(_ *testing.T, manager *SessionManager, _ *time.Time, _ control.UserSummary, _ *Session) {
				manager.Close()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := openMiddlewareTestInstance(t)
			config := securityTestSessionConfig()
			if test.name == "oldest eviction" {
				config.MaxConcurrent = 1
			}
			start := time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC)
			manager, clock := newClockedSessionManager(t, config, start)
			user := testControlUser(testVaultSessionUserID)
			lifecycle := newFakeVaultLifecycle(t, instance, user.ID)
			session := createTestVaultSession(t, manager, user, lifecycle.root())
			store := &fakeCurrentUserStore{user: user}
			entered := make(chan struct{})
			allowReturn := make(chan struct{})
			handler := VaultSessionAuthMiddleware(manager, store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-allowReturn
				w.WriteHeader(http.StatusNoContent)
			}))

			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, vaultSessionRequest(t, manager, session))
				response <- recorder
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("long request did not enter")
			}

			test.trigger(t, manager, clock, user, session)
			closed := make(chan struct{})
			go func() {
				lifecycle.closeUser()
				close(closed)
			}()
			select {
			case <-closed:
				t.Fatal("vault manager close completed before request child release")
			case <-time.After(25 * time.Millisecond):
			}
			denied := httptest.NewRecorder()
			handler.ServeHTTP(denied, vaultSessionRequest(t, manager, session))
			if denied.Code != http.StatusUnauthorized {
				t.Fatalf("new request status = %d, want 401", denied.Code)
			}

			close(allowReturn)
			select {
			case recorder := <-response:
				if recorder.Code != http.StatusNoContent {
					t.Fatalf("long request status = %d, want 204", recorder.Code)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("long request did not finish")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("vault manager close did not finish")
			}
			references, roots, borrows, children, duplicates := lifecycle.counts()
			if references != 0 || roots != 1 || borrows != 1 || children != 1 || duplicates != 0 {
				t.Fatalf("lifecycle refs=%d roots=%d borrows=%d children=%d duplicates=%d", references, roots, borrows, children, duplicates)
			}
		})
	}
}

func TestVaultSessionRotateDeleteRaceReleasesRootOnce(t *testing.T) {
	for iteration := range 100 {
		manager := NewSessionManagerWithConfig(securityTestSessionConfig())
		user := testControlUser(testVaultSessionUserID)
		var released atomic.Int32
		session := createTestVaultSession(t, manager, user, countingSessionRoot(user.ID, &released))
		start := make(chan struct{})
		rotated := make(chan *Session, 1)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			result, err := manager.RotateAfterReauthentication(session.ID)
			if err != nil {
				rotated <- nil
				return
			}
			rotated <- result
		}()
		go func() {
			defer wait.Done()
			<-start
			manager.DeleteSession(session.ID)
		}()
		close(start)
		wait.Wait()
		result := <-rotated
		if result != nil {
			manager.DeleteSession(result.ID)
		}
		manager.Close()
		if got := released.Load(); got != 1 {
			t.Fatalf("iteration %d: root releases = %d, want 1", iteration, got)
		}
	}
}
