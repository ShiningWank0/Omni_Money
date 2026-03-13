// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// SessionCookieName はセッション識別子を保存するCookie名
	SessionCookieName = "omni_money_session"
	// DefaultSessionMaxAge はセッション有効期限の既定値（24時間）
	DefaultSessionMaxAge = 24 * time.Hour
)

type sessionContextKey string

const sessionKey sessionContextKey = "session"

// Session はサーバー側で保持するセッション情報
type Session struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionManager はインメモリのセッションストア
// Agent.md §6.4.1 に従い sync.Map で管理する
type SessionManager struct {
	maxAge   time.Duration
	sessions sync.Map // map[string]Session
}

// NewSessionManager はSessionManagerを生成する
func NewSessionManager(maxAge time.Duration) *SessionManager {
	if maxAge <= 0 {
		maxAge = DefaultSessionMaxAge
	}
	sm := &SessionManager{maxAge: maxAge}
	go sm.cleanupLoop()
	return sm
}

// cleanupLoop は期限切れセッションを定期的に削除する（メモリリーク防止）
func (m *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		m.sessions.Range(func(key, value interface{}) bool {
			session, ok := value.(Session)
			if !ok || now.After(session.ExpiresAt) {
				m.sessions.Delete(key)
			}
			return true
		})
	}
}

// MaxAge はセッション有効期限を返す
func (m *SessionManager) MaxAge() time.Duration {
	return m.maxAge
}

// CreateSession は新しいセッションを作成する
func (m *SessionManager) CreateSession(username string) (*Session, error) {
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	session := Session{
		ID:        sessionID,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(m.maxAge),
	}
	m.sessions.Store(sessionID, session)
	return &session, nil
}

func generateSessionID() (string, error) {
	// 32バイト以上のランダム値を16進文字列化（Agent.md §6.4.1）
	buf := make([]byte, 32)
	n, err := rand.Read(buf)
	if err != nil {
		return "", err
	}
	if n != 32 {
		return "", fmt.Errorf("セッションID生成に必要なランダムバイト数を取得できませんでした: %d/32", n)
	}
	return hex.EncodeToString(buf), nil
}

// GetSession はセッションIDからセッションを取得する
func (m *SessionManager) GetSession(sessionID string) (*Session, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, false
	}

	raw, ok := m.sessions.Load(sessionID)
	if !ok {
		return nil, false
	}

	session, ok := raw.(Session)
	if !ok {
		m.sessions.Delete(sessionID)
		return nil, false
	}

	if time.Now().After(session.ExpiresAt) {
		m.sessions.Delete(sessionID)
		return nil, false
	}

	return &session, true
}

// DeleteSession はセッションを削除する
func (m *SessionManager) DeleteSession(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	m.sessions.Delete(sessionID)
}

// GetSessionFromRequest はCookieからセッションを取得する
func (m *SessionManager) GetSessionFromRequest(r *http.Request) (*Session, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, false
	}
	return m.GetSession(cookie.Value)
}

// SetSessionCookie はセッションCookieを設定する
func (m *SessionManager) SetSessionCookie(w http.ResponseWriter, r *http.Request, session *Session) {
	if session == nil {
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    session.ID,
		Path:     "/",
		Expires:  session.ExpiresAt,
		MaxAge:   int(m.maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   RequestProto(r) == "https",
	})
}

// ClearSessionCookie はセッションCookieを削除する
func (m *SessionManager) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   RequestProto(r) == "https",
	})
}

// SessionAuthMiddleware はサーバーモードAPIのセッション認証を強制する
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

	// AI APIは独自トークン認証を使用
	if strings.HasPrefix(path, "/api/v1/ai/") {
		return false
	}

	// 非APIパス（静的ファイル配信）は認証不要
	if !strings.HasPrefix(path, "/api/") {
		return false
	}

	// 認証APIの例外
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
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":          "認証が必要です",
		"login_required": true,
	})
}

// SessionFromContext はリクエストコンテキストからセッションを取得する
func SessionFromContext(ctx context.Context) (*Session, bool) {
	raw := ctx.Value(sessionKey)
	session, ok := raw.(*Session)
	if !ok || session == nil {
		return nil, false
	}
	return session, true
}
