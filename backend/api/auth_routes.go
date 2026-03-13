// Package api はAPIの接続口定義と通信経路（ルーティング）を提供する
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"omni_money/backend/middleware"
)

func handleAuthLogin(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		if !authManager.PasswordConfigured() {
			jsonError(w, "サーバー設定エラー: 管理者に連絡してください", http.StatusInternalServerError)
			return
		}

		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
			return
		}
		req.Password = strings.TrimSpace(req.Password)
		if req.Password == "" {
			jsonError(w, "パスワードを入力してください", http.StatusBadRequest)
			return
		}

		clientIP := middleware.ClientIPFromRequest(r)
		if locked, remaining := authManager.IsIPLocked(clientIP); locked {
			jsonResponse(w, map[string]interface{}{
				"error":               "ログイン試行回数が上限に達しました。しばらくしてから再試行してください。",
				"retry_after_seconds": int(remaining.Seconds()),
			}, http.StatusTooManyRequests)
			return
		}

		if !authManager.VerifyPassword(req.Password) {
			authManager.RecordLoginAttempt(clientIP, false)
			jsonResponse(w, map[string]interface{}{
				"error":              "パスワードが正しくありません",
				"remaining_attempts": authManager.RemainingAttempts(clientIP),
			}, http.StatusUnauthorized)
			return
		}

		authManager.RecordLoginAttempt(clientIP, true)

		session, err := authManager.CreateSession("user")
		if err != nil {
			jsonError(w, "セッション作成に失敗しました", http.StatusInternalServerError)
			return
		}
		authManager.SessionManager().SetSessionCookie(w, r, session)

		jsonResponse(w, map[string]interface{}{
			"authenticated": true,
			"message":       "ログインしました",
			"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		}, http.StatusOK)
	}
}

func handleAuthLogout(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		if cookie, err := r.Cookie(middleware.SessionCookieName); err == nil {
			authManager.SessionManager().DeleteSession(cookie.Value)
		}
		authManager.SessionManager().ClearSessionCookie(w, r)

		jsonResponse(w, map[string]interface{}{
			"success": true,
			"message": "ログアウトしました",
		}, http.StatusOK)
	}
}

func handleAuthStatus(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		session, ok := authManager.SessionManager().GetSessionFromRequest(r)
		if !ok {
			jsonResponse(w, map[string]bool{"authenticated": false}, http.StatusOK)
			return
		}

		jsonResponse(w, map[string]interface{}{
			"authenticated": true,
			"username":      session.Username,
			"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		}, http.StatusOK)
	}
}
