// Package middleware は認証、AI用APIの接続制御を提供する。
package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/core"
	"omni_money/backend/vault"
)

const (
	// SessionCookieName is retained for loopback HTTP development. Public HTTPS
	// requests use the __Host- prefixed name below.
	SessionCookieName       = "omni_money_session"
	SecureSessionCookieName = "__Host-omni_money_session"
	CSRFHeaderName          = "X-CSRF-Token"

	DefaultSessionMaxAge = 8 * time.Hour
	// Financial data is high-value personal information. Fifteen minutes keeps
	// unattended browser sessions from remaining usable for the longer default
	// while still avoiding an overly disruptive timeout for normal use.
	DefaultSessionIdleTimeout   = 15 * time.Minute
	DefaultRecentAuthAge        = 5 * time.Minute
	DefaultMaxConcurrent        = 3
	sessionCleanupInterval      = 5 * time.Minute
	minSessionMaxAge            = time.Hour
	maxSessionMaxAge            = 7 * 24 * time.Hour
	minSessionIdleTimeout       = 5 * time.Minute
	maxSessionIdleTimeout       = 2 * time.Hour
	minRecentAuthAge            = time.Minute
	maxRecentAuthAge            = 30 * time.Minute
	maxConcurrentSessionCeiling = 10
)

var (
	errSessionNotFound      = errors.New("session is missing or expired")
	ErrSessionManagerClosed = errors.New("session manager is closed")
	ErrInvalidVaultSession  = errors.New("vault session binding is invalid")
)

type sessionContextKey struct{}
type authenticatedUserContextKey struct{}
type coreServiceContextKey struct{}
type snapshotServiceContextKey struct{}

var sessionKey sessionContextKey

// SessionConfig bounds both session lifetime and in-memory resource use.
type SessionConfig struct {
	MaxAge        time.Duration
	IdleTimeout   time.Duration
	RecentAuthAge time.Duration
	MaxConcurrent int
}

func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		MaxAge:        DefaultSessionMaxAge,
		IdleTimeout:   DefaultSessionIdleTimeout,
		RecentAuthAge: DefaultRecentAuthAge,
		MaxConcurrent: DefaultMaxConcurrent,
	}
}

// SessionConfigFromEnv parses security-sensitive limits strictly. Invalid or
// unreasonably broad values are startup errors rather than silent fallbacks.
func SessionConfigFromEnv() (SessionConfig, error) {
	cfg := DefaultSessionConfig()
	var err error
	if cfg.MaxAge, err = durationEnv("SESSION_MAX_AGE_HOURS", time.Hour, cfg.MaxAge, minSessionMaxAge, maxSessionMaxAge); err != nil {
		return SessionConfig{}, err
	}
	if cfg.IdleTimeout, err = durationEnv("SESSION_IDLE_TIMEOUT_MINUTES", time.Minute, cfg.IdleTimeout, minSessionIdleTimeout, maxSessionIdleTimeout); err != nil {
		return SessionConfig{}, err
	}
	if cfg.RecentAuthAge, err = durationEnv("SESSION_REAUTH_MAX_AGE_MINUTES", time.Minute, cfg.RecentAuthAge, minRecentAuthAge, maxRecentAuthAge); err != nil {
		return SessionConfig{}, err
	}

	rawConcurrent := strings.TrimSpace(os.Getenv("SESSION_MAX_CONCURRENT"))
	if rawConcurrent != "" {
		value, parseErr := strconv.Atoi(rawConcurrent)
		if parseErr != nil || value < 1 || value > maxConcurrentSessionCeiling {
			return SessionConfig{}, fmt.Errorf("SESSION_MAX_CONCURRENT must be between 1 and %d", maxConcurrentSessionCeiling)
		}
		cfg.MaxConcurrent = value
	}
	if cfg.IdleTimeout > cfg.MaxAge {
		return SessionConfig{}, errors.New("SESSION_IDLE_TIMEOUT_MINUTES must not exceed SESSION_MAX_AGE_HOURS")
	}
	if cfg.RecentAuthAge > cfg.IdleTimeout {
		return SessionConfig{}, errors.New("SESSION_REAUTH_MAX_AGE_MINUTES must not exceed the idle timeout")
	}
	return cfg, nil
}

func durationEnv(name string, unit, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	duration := time.Duration(value) * unit
	if duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return duration, nil
}

// Session is a request-scoped copy of server-side state. CSRFToken is returned
// only in authenticated no-store responses and never serialized implicitly.
type Session struct {
	ID                string       `json:"-"`
	Username          string       `json:"username"`
	UserID            string       `json:"user_id,omitempty"`
	Email             string       `json:"email,omitempty"`
	DisplayName       string       `json:"display_name,omitempty"`
	Role              control.Role `json:"role,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	LastSeenAt        time.Time    `json:"last_seen_at"`
	ExpiresAt         time.Time    `json:"expires_at"`
	ReauthenticatedAt time.Time    `json:"-"`
	CSRFToken         string       `json:"-"`
}

type sessionRecord struct {
	session Session
	root    *sessionVaultRoot
}

// sessionVaultRoot is owned by exactly one sessionRecord after successful
// creation. The callbacks keep lifecycle bookkeeping testable without
// exporting constructors or retaining a raw vault lease beyond its closure.
type sessionVaultRoot struct {
	userID       string
	validate     func() error
	borrow       func() (*requestVaultLease, error)
	beginRestore func() (*vault.RestoreOperation, error)
	release      func()
	once         sync.Once
}

type requestVaultLease struct {
	service        *core.Service
	createSnapshot func() (string, error)
	listSnapshots  func() ([]string, error)
	release        func()
	once           sync.Once
	mu             sync.RWMutex
}

// requestVaultLeaseRelease is shared by the middleware defer and handlers
// that materialize a response before streaming it. It makes early release
// explicit while retaining exactly-once semantics if the defer runs too.
type requestVaultLeaseRelease struct {
	once    sync.Once
	release func()
}

func (release *requestVaultLeaseRelease) Release() {
	if release == nil {
		return
	}
	release.once.Do(func() {
		if release.release != nil {
			release.release()
		}
	})
}

type requestVaultLeaseReleaseContextKey struct{}

func newSessionVaultRoot(lease *vault.Lease) (*sessionVaultRoot, error) {
	if lease == nil || lease.UserID() == "" || lease.VaultID() == "" {
		return nil, ErrInvalidVaultSession
	}
	if _, err := lease.Service(); err != nil {
		return nil, ErrInvalidVaultSession
	}
	root := &sessionVaultRoot{
		userID:       lease.UserID(),
		validate:     lease.ValidateSessionRootLocked,
		release:      lease.Release,
		beginRestore: lease.BeginRestore,
	}
	root.borrow = func() (*requestVaultLease, error) {
		child, err := lease.Borrow()
		if err != nil {
			return nil, err
		}
		service, err := child.Service()
		if err != nil {
			child.Release()
			return nil, ErrInvalidVaultSession
		}
		return &requestVaultLease{
			service:        service,
			createSnapshot: child.CreateSnapshot,
			listSnapshots:  child.ListSnapshots,
			release:        child.Release,
		}, nil
	}
	return root, nil
}

func (root *sessionVaultRoot) Borrow() (*requestVaultLease, error) {
	if root == nil || root.borrow == nil {
		return nil, ErrInvalidVaultSession
	}
	return root.borrow()
}

func (root *sessionVaultRoot) Release() {
	if root == nil {
		return
	}
	root.once.Do(func() {
		if root.release != nil {
			root.release()
		}
		root.borrow = nil
		root.validate = nil
		root.beginRestore = nil
		root.release = nil
	})
}

func (root *sessionVaultRoot) BeginRestore() (*vault.RestoreOperation, error) {
	if root == nil || root.beginRestore == nil {
		return nil, ErrInvalidVaultSession
	}
	return root.beginRestore()
}

func (lease *requestVaultLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		if lease.release != nil {
			lease.release()
		}
		lease.service = nil
		lease.createSnapshot = nil
		lease.listSnapshots = nil
		lease.release = nil
	})
}

func (lease *requestVaultLease) Service() *core.Service {
	if lease == nil {
		return nil
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	return lease.service
}

func (lease *requestVaultLease) CreateSnapshot() (string, error) {
	if lease == nil {
		return "", vault.ErrLeaseReleased
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.release == nil || lease.createSnapshot == nil {
		return "", vault.ErrLeaseReleased
	}
	return lease.createSnapshot()
}

func (lease *requestVaultLease) ListSnapshots() ([]string, error) {
	if lease == nil {
		return nil, vault.ErrLeaseReleased
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.release == nil || lease.listSnapshots == nil {
		return nil, vault.ErrLeaseReleased
	}
	return lease.listSnapshots()
}

// SessionManager uses one mutex for lookup/touch/delete/rotation. This avoids
// a Load-then-Store race that could resurrect a session after logout.
type SessionManager struct {
	config     SessionConfig
	mu         sync.Mutex
	sessions   map[string]*sessionRecord
	now        func() time.Time
	done       chan struct{}
	closedDone chan struct{}
	closeOnce  sync.Once
	closed     bool
}

// NewSessionManager preserves the previous test-facing constructor while
// applying the new secure idle/concurrency defaults.
func NewSessionManager(maxAge time.Duration) *SessionManager {
	cfg := DefaultSessionConfig()
	if maxAge > 0 {
		cfg.MaxAge = maxAge
		if cfg.IdleTimeout > maxAge {
			cfg.IdleTimeout = maxAge
		}
		if cfg.RecentAuthAge > cfg.IdleTimeout {
			cfg.RecentAuthAge = cfg.IdleTimeout
		}
	}
	return NewSessionManagerWithConfig(cfg)
}

func NewSessionManagerWithConfig(config SessionConfig) *SessionManager {
	defaults := DefaultSessionConfig()
	if config.MaxAge <= 0 {
		config.MaxAge = defaults.MaxAge
	}
	if config.IdleTimeout <= 0 || config.IdleTimeout > config.MaxAge {
		config.IdleTimeout = minDuration(defaults.IdleTimeout, config.MaxAge)
	}
	if config.RecentAuthAge <= 0 || config.RecentAuthAge > config.IdleTimeout {
		config.RecentAuthAge = minDuration(defaults.RecentAuthAge, config.IdleTimeout)
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = defaults.MaxConcurrent
	}
	sm := &SessionManager{
		config:     config,
		sessions:   make(map[string]*sessionRecord),
		now:        time.Now,
		done:       make(chan struct{}),
		closedDone: make(chan struct{}),
	}
	go sm.cleanupLoop()
	return sm
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (m *SessionManager) Close() {
	_ = m.CloseContext(context.Background())
}

// CloseContext invalidates every session immediately, then releases root
// vault leases within the caller's shutdown budget. Release continues in the
// background after a deadline so later cleanup stages are never skipped.
func (m *SessionManager) CloseContext(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.closeOnce.Do(func() {
		close(m.done)
		m.mu.Lock()
		m.closed = true
		roots := make([]*sessionVaultRoot, 0, len(m.sessions))
		for id := range m.sessions {
			roots = append(roots, m.deleteRecordLocked(id))
		}
		m.mu.Unlock()
		go func() {
			releaseSessionVaultRoots(roots)
			close(m.closedDone)
		}()
	})
	select {
	case <-m.closedDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IdleTimeoutSeconds returns the server-enforced inactivity timeout in whole
// seconds for client-side screen-lock scheduling. The configuration is
// immutable after construction; the mutex still makes this accessor safe if
// the manager's lifecycle is inspected concurrently with cleanup.
func (m *SessionManager) IdleTimeoutSeconds() int64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(m.config.IdleTimeout / time.Second)
}

func (m *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.mu.Lock()
			roots := m.purgeExpiredLocked(m.now())
			m.mu.Unlock()
			releaseSessionVaultRoots(roots)
		}
	}
}

func (m *SessionManager) MaxAge() time.Duration { return m.config.MaxAge }

func (m *SessionManager) CreateSession(username string) (*Session, error) {
	return m.createSession(username, nil, nil)
}

// CreateVaultSession creates a multi-user session that owns root after it
// returns successfully. On every error, ownership remains with the caller,
// which must release root. The caller must not release root after success.
func (m *SessionManager) CreateVaultSession(user control.UserSummary, root *vault.Lease) (*Session, error) {
	resource, err := newSessionVaultRoot(root)
	if err != nil {
		return nil, err
	}
	if user.ID == "" || user.Email == "" || user.State != control.UserActive ||
		(user.Role != control.RoleAdmin && user.Role != control.RoleUser) || resource.userID != user.ID {
		return nil, ErrInvalidVaultSession
	}
	return m.createSession(user.Email, &user, resource)
}

func (m *SessionManager) createSession(username string, user *control.UserSummary, root *sessionVaultRoot) (*Session, error) {
	if m == nil {
		return nil, ErrSessionManagerClosed
	}
	sessionID, csrfToken, err := generateSessionSecrets()
	if err != nil {
		return nil, err
	}
	now := m.now()
	session := Session{
		ID:                sessionID,
		Username:          username,
		CreatedAt:         now,
		LastSeenAt:        now,
		ExpiresAt:         now.Add(m.config.MaxAge),
		ReauthenticatedAt: now,
		CSRFToken:         csrfToken,
	}
	if user != nil {
		session.UserID = user.ID
		session.Email = user.Email
		session.DisplayName = user.DisplayName
		session.Role = user.Role
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrSessionManagerClosed
	}
	// SessionManager's mutex is held while the exact vault entry is checked.
	// BeginRestore takes this same lock before marking the entry draining, so a
	// login that loses the race cannot publish a root after DeleteAllSessions.
	if root != nil && root.validate != nil {
		if err := root.validate(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
	}
	roots := m.purgeExpiredLocked(now)
	roots = append(roots, m.evictOldestForOwnerLocked(sessionOwnerKey(session))...)
	m.sessions[sessionID] = &sessionRecord{session: session, root: root}
	result := cloneSession(&session)
	m.mu.Unlock()
	releaseSessionVaultRoots(roots)
	return result, nil
}

func generateSessionSecrets() (string, string, error) {
	sessionID, err := randomHex(32)
	if err != nil {
		return "", "", fmt.Errorf("generate session ID: %w", err)
	}
	csrfToken, err := randomHex(32)
	if err != nil {
		return "", "", fmt.Errorf("generate CSRF token: %w", err)
	}
	return sessionID, csrfToken, nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	n, err := rand.Read(buf)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("short random read: %d/%d", n, size)
	}
	return hex.EncodeToString(buf), nil
}

func (m *SessionManager) GetSession(sessionID string) (*Session, bool) {
	if m == nil || !isCanonicalSessionSecret(sessionID) {
		return nil, false
	}
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, false
	}
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) {
		root := m.deleteRecordLocked(sessionID)
		m.mu.Unlock()
		root.Release()
		return nil, false
	}
	record.session.LastSeenAt = now
	result := cloneSession(&record.session)
	m.mu.Unlock()
	return result, true
}

func (m *SessionManager) DeleteSession(sessionID string) {
	if m == nil || !isCanonicalSessionSecret(sessionID) {
		return
	}
	m.mu.Lock()
	root := m.deleteRecordLocked(sessionID)
	m.mu.Unlock()
	root.Release()
}

func (m *SessionManager) DeleteAllSessions(username string) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	deleted := 0
	var roots []*sessionVaultRoot
	for id, record := range m.sessions {
		if record.session.Username == username {
			roots = append(roots, m.deleteRecordLocked(id))
			deleted++
		}
	}
	m.mu.Unlock()
	releaseSessionVaultRoots(roots)
	return deleted
}

// DeleteAllSessionsForUser invalidates sessions by the authenticated control
// user ID. Server callers should use this rather than accepting an email or
// username supplied by a request body.
func (m *SessionManager) DeleteAllSessionsForUser(userID string) int {
	if m == nil || userID == "" {
		return 0
	}
	m.mu.Lock()
	deleted := 0
	var roots []*sessionVaultRoot
	for id, record := range m.sessions {
		if record.session.UserID == userID {
			roots = append(roots, m.deleteRecordLocked(id))
			deleted++
		}
	}
	m.mu.Unlock()
	releaseSessionVaultRoots(roots)
	return deleted
}

// BeginRestore atomically obtains a root-only restore capability from the
// authenticated session's exact session record.  It never accepts a target
// user or vault identifier from a request.  The caller must invalidate all
// sessions for the returned operation's user after this method succeeds.
func (m *SessionManager) BeginRestore(sessionID string) (*vault.RestoreOperation, string, error) {
	if m == nil || !isCanonicalSessionSecret(sessionID) {
		return nil, "", errSessionNotFound
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, "", errSessionNotFound
	}
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) || record.root == nil || record.session.UserID == "" {
		return nil, "", errSessionNotFound
	}
	operation, err := record.root.BeginRestore()
	if err != nil {
		return nil, "", err
	}
	return operation, record.session.UserID, nil
}

// RotateAfterReauthentication invalidates both the old session ID and old
// CSRF token atomically without extending the absolute lifetime.
func (m *SessionManager) RotateAfterReauthentication(oldSessionID string) (*Session, error) {
	if m == nil || !isCanonicalSessionSecret(oldSessionID) {
		return nil, errSessionNotFound
	}
	newID, newCSRF, err := generateSessionSecrets()
	if err != nil {
		return nil, err
	}
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errSessionNotFound
	}
	record, ok := m.sessions[oldSessionID]
	if !ok || m.expiredLocked(record, now) {
		root := m.deleteRecordLocked(oldSessionID)
		m.mu.Unlock()
		root.Release()
		return nil, errSessionNotFound
	}
	rotated := record.session
	rotated.ID = newID
	rotated.CSRFToken = newCSRF
	rotated.LastSeenAt = now
	rotated.ReauthenticatedAt = now
	// Move the existing record itself so its root lease remains owned exactly
	// once and is never released during credential rotation.
	delete(m.sessions, oldSessionID)
	record.session = rotated
	m.sessions[newID] = record
	result := cloneSession(&rotated)
	m.mu.Unlock()
	return result, nil
}

func (m *SessionManager) ValidateCSRF(sessionID, token string) bool {
	if m == nil || !isCanonicalSessionSecret(sessionID) || !isCanonicalSessionSecret(token) {
		return false
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, m.now()) {
		root := m.deleteRecordLocked(sessionID)
		m.mu.Unlock()
		root.Release()
		return false
	}
	valid := subtle.ConstantTimeCompare([]byte(record.session.CSRFToken), []byte(token)) == 1
	m.mu.Unlock()
	return valid
}

func isCanonicalSessionSecret(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (m *SessionManager) IsRecent(sessionID string) bool {
	if m == nil || !isCanonicalSessionSecret(sessionID) {
		return false
	}
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) {
		root := m.deleteRecordLocked(sessionID)
		m.mu.Unlock()
		root.Release()
		return false
	}
	recent := now.Sub(record.session.ReauthenticatedAt) <= m.config.RecentAuthAge
	m.mu.Unlock()
	return recent
}

func (m *SessionManager) expiredLocked(record *sessionRecord, now time.Time) bool {
	if record == nil {
		return true
	}
	return !now.Before(record.session.ExpiresAt) || now.Sub(record.session.LastSeenAt) >= m.config.IdleTimeout
}

func (m *SessionManager) purgeExpiredLocked(now time.Time) []*sessionVaultRoot {
	var roots []*sessionVaultRoot
	for id, record := range m.sessions {
		if m.expiredLocked(record, now) {
			roots = append(roots, m.deleteRecordLocked(id))
		}
	}
	return roots
}

func (m *SessionManager) evictOldestForOwnerLocked(owner string) []*sessionVaultRoot {
	var roots []*sessionVaultRoot
	for {
		count := 0
		oldestID := ""
		var oldest time.Time
		for id, record := range m.sessions {
			if sessionOwnerKey(record.session) != owner {
				continue
			}
			count++
			if oldestID == "" || record.session.CreatedAt.Before(oldest) {
				oldestID = id
				oldest = record.session.CreatedAt
			}
		}
		if count < m.config.MaxConcurrent || oldestID == "" {
			return roots
		}
		roots = append(roots, m.deleteRecordLocked(oldestID))
	}
}

func sessionOwnerKey(session Session) string {
	if session.UserID != "" {
		return "user-id\x00" + session.UserID
	}
	return "username\x00" + session.Username
}

// deleteRecordLocked removes a record and transfers ownership of its root to
// the caller. The caller must unlock SessionManager.mu before invoking Release.
func (m *SessionManager) deleteRecordLocked(sessionID string) *sessionVaultRoot {
	record := m.sessions[sessionID]
	if record == nil {
		return nil
	}
	delete(m.sessions, sessionID)
	root := record.root
	record.root = nil
	return root
}

func releaseSessionVaultRoots(roots []*sessionVaultRoot) {
	for _, root := range roots {
		root.Release()
	}
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	copy := *session
	return &copy
}

// GetSessionFromRequest rejects duplicate/shadow cookies and requires the
// __Host- cookie on HTTPS.
func (m *SessionManager) GetSessionFromRequest(r *http.Request) (*Session, bool) {
	if m == nil || r == nil {
		return nil, false
	}
	sessionID, err := sessionIDFromRequest(r)
	if err != nil {
		return nil, false
	}
	return m.GetSession(sessionID)
}

func (m *SessionManager) borrowVaultSessionFromRequest(r *http.Request) (*Session, *requestVaultLease, bool) {
	if m == nil || r == nil {
		return nil, nil, false
	}
	sessionID, err := sessionIDFromRequest(r)
	if err != nil || !isCanonicalSessionSecret(sessionID) {
		return nil, nil, false
	}
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, false
	}
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) {
		root := m.deleteRecordLocked(sessionID)
		m.mu.Unlock()
		root.Release()
		return nil, nil, false
	}
	if record.root == nil || record.session.UserID == "" {
		m.mu.Unlock()
		return nil, nil, false
	}
	child, err := record.root.Borrow()
	if err != nil {
		root := m.deleteRecordLocked(sessionID)
		m.mu.Unlock()
		root.Release()
		return nil, nil, false
	}
	record.session.LastSeenAt = now
	result := cloneSession(&record.session)
	m.mu.Unlock()
	return result, child, true
}

func (m *SessionManager) sessionForVaultRequest(r *http.Request) (*Session, bool) {
	if m == nil || r == nil {
		return nil, false
	}
	session, ok := m.GetSessionFromRequest(r)
	if !ok || session.UserID == "" {
		return nil, false
	}
	return session, true
}

func sessionIDFromRequest(r *http.Request) (string, error) {
	expected := SessionCookieName
	unexpected := SecureSessionCookieName
	if RequestProto(r) == "https" {
		expected, unexpected = SecureSessionCookieName, SessionCookieName
	}
	found := ""
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == unexpected {
			return "", errors.New("unexpected session cookie")
		}
		if cookie.Name == expected {
			count++
			found = cookie.Value
		}
	}
	if count != 1 || found == "" {
		return "", errors.New("missing or duplicate session cookie")
	}
	return found, nil
}

func (m *SessionManager) SetSessionCookie(w http.ResponseWriter, r *http.Request, session *Session) {
	if session == nil || !isCanonicalSessionSecret(session.ID) {
		return
	}
	secure := RequestProto(r) == "https"
	name := SessionCookieName
	if secure {
		name = SecureSessionCookieName
	}
	maxAge := int(session.ExpiresAt.Sub(m.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	// #nosec G124 -- Secure is conditional only for loopback HTTP development;
	// trusted HTTPS/X-Forwarded-Proto requests always use Secure + __Host-.
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    session.ID,
		Path:     "/",
		Expires:  session.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}

func (m *SessionManager) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := RequestProto(r) == "https"
	// #nosec G124 -- these are deletion cookies; all security attributes are
	// populated below before they are emitted, including Secure for __Host-.
	for _, cookie := range []http.Cookie{
		{Name: SessionCookieName, Secure: secure},
		{Name: SecureSessionCookieName, Secure: true},
	} {
		cookie.Value = ""
		cookie.Path = "/"
		cookie.Expires = time.Unix(0, 0)
		cookie.MaxAge = -1
		cookie.HttpOnly = true
		cookie.SameSite = http.SameSiteStrictMode
		http.SetCookie(w, &cookie)
	}
}

// SessionAuthMiddleware authenticates API requests and installs a request
// scoped copy before CSRF/recent-auth middleware runs.
func SessionAuthMiddleware(sessionManager *SessionManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresSessionAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := sessionManager.GetSessionFromRequest(r)
		if !ok {
			writeAuthRequired(w)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CurrentUserStore is the minimum control-plane lookup required by the
// request-scoped vault middleware. control.Store implements it directly.
type CurrentUserStore interface {
	GetUser(context.Context, string) (control.UserSummary, error)
}

// VaultSessionAuthMiddleware authenticates a multi-user session, borrows a
// request child from its server-owned root lease, refreshes control-plane user
// state/role, and installs the bound core Service. The child is released even
// when the handler panics. The legacy SessionAuthMiddleware remains available
// for Desktop and existing single-user server tests.
func VaultSessionAuthMiddleware(sessionManager *SessionManager, users CurrentUserStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresSessionAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Restore is intentionally the one route that authenticates the identity
		// without borrowing a child lease.  The handler later acquires the
		// root-only restore operation after CSRF and recent-auth checks; borrowing
		// here would leave the operation waiting on this very request.
		if isSnapshotRestoreRequest(r) {
			session, ok := sessionManager.sessionForVaultRequest(r)
			if !ok {
				writeAuthRequired(w)
				return
			}
			currentUser, ok := currentVaultUser(w, r, sessionManager, users, session.UserID)
			if !ok {
				return
			}
			ctx := context.WithValue(r.Context(), sessionKey, session)
			ctx = context.WithValue(ctx, authenticatedUserContextKey{}, currentUser)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		session, child, ok := sessionManager.borrowVaultSessionFromRequest(r)
		if !ok {
			writeAuthRequired(w)
			return
		}
		leaseRelease := &requestVaultLeaseRelease{release: child.Release}
		defer leaseRelease.Release()

		currentUser, ok := currentVaultUser(w, r, sessionManager, users, session.UserID)
		if !ok {
			return
		}
		session.Username = currentUser.Email
		session.Email = currentUser.Email
		session.DisplayName = currentUser.DisplayName
		session.Role = currentUser.Role
		service := child.Service()
		if service == nil {
			sessionManager.DeleteAllSessionsForUser(session.UserID)
			writeVaultRoutingUnavailable(w)
			return
		}

		ctx := context.WithValue(r.Context(), sessionKey, session)
		ctx = context.WithValue(ctx, authenticatedUserContextKey{}, currentUser)
		// Keep the lease, rather than a detached *Service, in the context so an
		// early release also makes CoreServiceFromContext fail closed during a
		// long response stream.
		ctx = context.WithValue(ctx, coreServiceContextKey{}, child)
		ctx = context.WithValue(ctx, requestVaultLeaseReleaseContextKey{}, leaseRelease)
		ctx = context.WithValue(ctx, snapshotServiceContextKey{}, child)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ReleaseRequestVaultLease releases the request-scoped vault child when a
// handler has finished all database work and is only streaming a materialized
// response. It is safe to call more than once and is a no-op for legacy
// single-user requests.
func ReleaseRequestVaultLease(ctx context.Context) {
	if ctx == nil {
		return
	}
	release, _ := ctx.Value(requestVaultLeaseReleaseContextKey{}).(*requestVaultLeaseRelease)
	if release != nil {
		release.Release()
	}
}

func currentVaultUser(w http.ResponseWriter, r *http.Request, sessionManager *SessionManager, users CurrentUserStore, userID string) (control.UserSummary, bool) {
	if users == nil {
		writeVaultRoutingUnavailable(w)
		return control.UserSummary{}, false
	}
	currentUser, err := users.GetUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			sessionManager.DeleteAllSessionsForUser(userID)
			writeAuthRequired(w)
			return control.UserSummary{}, false
		}
		writeVaultRoutingUnavailable(w)
		return control.UserSummary{}, false
	}
	if currentUser.ID != userID || currentUser.State != control.UserActive ||
		(currentUser.Role != control.RoleAdmin && currentUser.Role != control.RoleUser) {
		sessionManager.DeleteAllSessionsForUser(userID)
		writeAuthRequired(w)
		return control.UserSummary{}, false
	}
	return currentUser, true
}

func isSnapshotRestoreRequest(r *http.Request) bool {
	return r != nil && r.URL.Path == "/api/snapshots/restore" && r.Method == http.MethodPost
}

func writeVaultRoutingUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": "ユーザーデータを安全に開けません",
	})
}

func requiresSessionAuth(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	if isPublicServerAuthRequest(r) {
		return false
	}
	return true
}

// isPublicServerAuthRequest is an exact method/path allowlist. Tokens and
// account secrets are accepted only in bounded POST bodies; a prefix match or
// trailing-slash variant must never become an authentication bypass.
func isPublicServerAuthRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch {
	case r.URL.Path == "/api/auth/status" && r.Method == http.MethodGet:
		return true
	case r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost:
		return true
	case (r.URL.Path == "/api/auth/passkeys/login/begin" || r.URL.Path == "/api/auth/passkeys/login/finish") && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/auth/setup" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/auth/invitations/accept" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/auth/password-reset/complete" && r.Method == http.MethodPost:
		return true
	default:
		return false
	}
}

func writeAuthRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":          "認証が必要です",
		"login_required": true,
	})
}

func SessionFromContext(ctx context.Context) (*Session, bool) {
	if ctx == nil {
		return nil, false
	}
	raw := ctx.Value(sessionKey)
	session, ok := raw.(*Session)
	if !ok || session == nil {
		return nil, false
	}
	return session, true
}

// AuthenticatedUserFromContext returns the current control-plane user loaded
// for this request, not the role cached when the session was created.
func AuthenticatedUserFromContext(ctx context.Context) (control.UserSummary, bool) {
	if ctx == nil {
		return control.UserSummary{}, false
	}
	user, ok := ctx.Value(authenticatedUserContextKey{}).(control.UserSummary)
	return user, ok && user.ID != ""
}

// CoreServiceFromContext returns the business service bound to the request's
// borrowed vault child. Financial handlers should use this service and never a
// caller-supplied user or vault identifier.
func CoreServiceFromContext(ctx context.Context) (*core.Service, bool) {
	if ctx == nil {
		return nil, false
	}
	switch bound := ctx.Value(coreServiceContextKey{}).(type) {
	case *core.Service: // legacy Desktop/global service binding
		return bound, bound != nil
	case *requestVaultLease: // request-scoped multi-user binding
		service := bound.Service()
		return service, service != nil
	default:
		return nil, false
	}
}

// SnapshotService is the request-bound snapshot capability.  It exposes no
// user/vault/path selection; the middleware supplies methods backed by the
// authenticated user's borrowed lease.
type SnapshotService interface {
	CreateSnapshot() (string, error)
	ListSnapshots() ([]string, error)
}

func SnapshotServiceFromContext(ctx context.Context) (SnapshotService, bool) {
	if ctx == nil {
		return nil, false
	}
	service, ok := ctx.Value(snapshotServiceContextKey{}).(SnapshotService)
	return service, ok && service != nil
}

// SessionMaxAgeFromEnv remains for source compatibility. New code should use
// SessionConfigFromEnv so invalid values fail closed.
func SessionMaxAgeFromEnv() time.Duration {
	cfg, err := SessionConfigFromEnv()
	if err != nil {
		return DefaultSessionMaxAge
	}
	return cfg.MaxAge
}
