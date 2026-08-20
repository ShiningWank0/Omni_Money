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
)

const (
	// SessionCookieName is retained for loopback HTTP development. Public HTTPS
	// requests use the __Host- prefixed name below.
	SessionCookieName       = "omni_money_session"
	SecureSessionCookieName = "__Host-omni_money_session"
	CSRFHeaderName          = "X-CSRF-Token"

	DefaultSessionMaxAge        = 8 * time.Hour
	DefaultSessionIdleTimeout   = 30 * time.Minute
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

var errSessionNotFound = errors.New("session is missing or expired")

type sessionContextKey string

const sessionKey sessionContextKey = "session"

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
	ID                string    `json:"-"`
	Username          string    `json:"username"`
	CreatedAt         time.Time `json:"created_at"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ReauthenticatedAt time.Time `json:"-"`
	CSRFToken         string    `json:"-"`
}

type sessionRecord struct {
	session Session
}

// SessionManager uses one mutex for lookup/touch/delete/rotation. This avoids
// a Load-then-Store race that could resurrect a session after logout.
type SessionManager struct {
	config    SessionConfig
	mu        sync.Mutex
	sessions  map[string]*sessionRecord
	now       func() time.Time
	done      chan struct{}
	closeOnce sync.Once
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
		config:   config,
		sessions: make(map[string]*sessionRecord),
		now:      time.Now,
		done:     make(chan struct{}),
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
	m.closeOnce.Do(func() { close(m.done) })
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
			m.purgeExpiredLocked(m.now())
			m.mu.Unlock()
		}
	}
}

func (m *SessionManager) MaxAge() time.Duration { return m.config.MaxAge }

func (m *SessionManager) CreateSession(username string) (*Session, error) {
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

	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(now)
	m.evictOldestForUserLocked(username)
	m.sessions[sessionID] = &sessionRecord{session: session}
	return cloneSession(&session), nil
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
	if !isCanonicalSessionSecret(sessionID) {
		return nil, false
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) {
		delete(m.sessions, sessionID)
		return nil, false
	}
	record.session.LastSeenAt = now
	return cloneSession(&record.session), true
}

func (m *SessionManager) DeleteSession(sessionID string) {
	if !isCanonicalSessionSecret(sessionID) {
		return
	}
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
}

func (m *SessionManager) DeleteAllSessions(username string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := 0
	for id, record := range m.sessions {
		if record.session.Username == username {
			delete(m.sessions, id)
			deleted++
		}
	}
	return deleted
}

// RotateAfterReauthentication invalidates both the old session ID and old
// CSRF token atomically without extending the absolute lifetime.
func (m *SessionManager) RotateAfterReauthentication(oldSessionID string) (*Session, error) {
	if !isCanonicalSessionSecret(oldSessionID) {
		return nil, errSessionNotFound
	}
	newID, newCSRF, err := generateSessionSecrets()
	if err != nil {
		return nil, err
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[oldSessionID]
	if !ok || m.expiredLocked(record, now) {
		delete(m.sessions, oldSessionID)
		return nil, errSessionNotFound
	}
	rotated := record.session
	rotated.ID = newID
	rotated.CSRFToken = newCSRF
	rotated.LastSeenAt = now
	rotated.ReauthenticatedAt = now
	delete(m.sessions, oldSessionID)
	m.sessions[newID] = &sessionRecord{session: rotated}
	return cloneSession(&rotated), nil
}

func (m *SessionManager) ValidateCSRF(sessionID, token string) bool {
	if !isCanonicalSessionSecret(sessionID) || !isCanonicalSessionSecret(token) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, m.now()) {
		delete(m.sessions, sessionID)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(record.session.CSRFToken), []byte(token)) == 1
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
	if !isCanonicalSessionSecret(sessionID) {
		return false
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[sessionID]
	if !ok || m.expiredLocked(record, now) {
		delete(m.sessions, sessionID)
		return false
	}
	return now.Sub(record.session.ReauthenticatedAt) <= m.config.RecentAuthAge
}

func (m *SessionManager) expiredLocked(record *sessionRecord, now time.Time) bool {
	if record == nil {
		return true
	}
	return !now.Before(record.session.ExpiresAt) || now.Sub(record.session.LastSeenAt) >= m.config.IdleTimeout
}

func (m *SessionManager) purgeExpiredLocked(now time.Time) {
	for id, record := range m.sessions {
		if m.expiredLocked(record, now) {
			delete(m.sessions, id)
		}
	}
}

func (m *SessionManager) evictOldestForUserLocked(username string) {
	for {
		count := 0
		oldestID := ""
		var oldest time.Time
		for id, record := range m.sessions {
			if record.session.Username != username {
				continue
			}
			count++
			if oldestID == "" || record.session.CreatedAt.Before(oldest) {
				oldestID = id
				oldest = record.session.CreatedAt
			}
		}
		if count < m.config.MaxConcurrent || oldestID == "" {
			return
		}
		delete(m.sessions, oldestID)
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
	sessionID, err := sessionIDFromRequest(r)
	if err != nil {
		return nil, false
	}
	return m.GetSession(sessionID)
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

func requiresSessionAuth(r *http.Request) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	if path == "/api/auth/login" && r.Method == http.MethodPost {
		return false
	}
	if path == "/api/auth/status" && r.Method == http.MethodGet {
		return false
	}
	return true
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
	raw := ctx.Value(sessionKey)
	session, ok := raw.(*Session)
	if !ok || session == nil {
		return nil, false
	}
	return session, true
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
