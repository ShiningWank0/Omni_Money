// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// LoginAttemptLimit は連続失敗許容回数
	LoginAttemptLimit = 5
	// LoginLockoutDuration はロック時間
	LoginLockoutDuration = 15 * time.Minute
)

type loginAttempt struct {
	Count       int
	LastAttempt time.Time
}

// AuthSessionManager はログイン試行制限とセッション生成を管理する
type AuthSessionManager struct {
	passwordHash   string
	sessionManager *SessionManager

	mu       sync.Mutex
	attempts map[string]loginAttempt // key: client IP
}

// NewAuthSessionManager はAuthSessionManagerを生成する
func NewAuthSessionManager(sessionManager *SessionManager, passwordHash string) *AuthSessionManager {
	return &AuthSessionManager{
		passwordHash:   strings.TrimSpace(passwordHash),
		sessionManager: sessionManager,
		attempts:       make(map[string]loginAttempt),
	}
}

// SessionManager はセッションマネージャを返す
func (a *AuthSessionManager) SessionManager() *SessionManager {
	return a.sessionManager
}

// PasswordConfigured はパスワードハッシュが設定されているかを返す
func (a *AuthSessionManager) PasswordConfigured() bool {
	return strings.TrimSpace(a.passwordHash) != ""
}

// VerifyPassword はbcryptでパスワードを検証する
func (a *AuthSessionManager) VerifyPassword(password string) bool {
	if !a.PasswordConfigured() {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(a.passwordHash), []byte(password)) == nil
}

// IsIPLocked はIPがロック中かを返す
func (a *AuthSessionManager) IsIPLocked(ip string) (bool, time.Duration) {
	ip = normalizeIPKey(ip)
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	attempt, ok := a.attempts[ip]
	if !ok {
		return false, 0
	}

	if attempt.Count < LoginAttemptLimit {
		return false, 0
	}

	elapsed := now.Sub(attempt.LastAttempt)
	if elapsed >= LoginLockoutDuration {
		delete(a.attempts, ip)
		return false, 0
	}

	return true, LoginLockoutDuration - elapsed
}

// RecordLoginAttempt はログイン試行を記録する
func (a *AuthSessionManager) RecordLoginAttempt(ip string, success bool) {
	ip = normalizeIPKey(ip)
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	if success {
		delete(a.attempts, ip)
		return
	}

	attempt := a.attempts[ip]
	attempt.Count++
	attempt.LastAttempt = now
	a.attempts[ip] = attempt
}

// RemainingAttempts は残り試行回数を返す
func (a *AuthSessionManager) RemainingAttempts(ip string) int {
	ip = normalizeIPKey(ip)

	a.mu.Lock()
	defer a.mu.Unlock()

	attempt, ok := a.attempts[ip]
	if !ok {
		return LoginAttemptLimit
	}
	if attempt.Count >= LoginAttemptLimit {
		return 0
	}
	return LoginAttemptLimit - attempt.Count
}

// CreateSession は認証済みユーザーのセッションを作成する
func (a *AuthSessionManager) CreateSession(username string) (*Session, error) {
	return a.sessionManager.CreateSession(username)
}

func normalizeIPKey(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	return ip
}

// SessionMaxAgeFromEnv は環境変数SESSION_MAX_AGE_HOURSからセッション有効期限を算出する
func SessionMaxAgeFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SESSION_MAX_AGE_HOURS"))
	if raw == "" {
		return DefaultSessionMaxAge
	}

	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		return DefaultSessionMaxAge
	}

	return time.Duration(hours) * time.Hour
}
