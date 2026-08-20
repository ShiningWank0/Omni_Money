package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/middleware"
)

const (
	maxAuthRequestBody = 4 * 1024
	maxPasswordBytes   = 72 // bcrypt's interoperable input limit
)

type authRequest struct {
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

func handleAuthLogin(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		request, ok := decodeAuthRequest(w, r)
		if !ok {
			return
		}
		clientIP := middleware.ClientIPFromRequest(r)
		if !reserveAuthAttempt(w, authManager, clientIP, "login") {
			return
		}
		valid, busy := authManager.VerifyCredentials(request.Password, request.TOTPCode)
		if busy {
			auditAuth("login_busy", clientIP, "")
			writeAuthRateLimited(w, time.Second)
			return
		}
		if !valid {
			authManager.RecordLoginAttempt(clientIP, false)
			auditAuth("login_failed", clientIP, "invalid_credentials")
			writeInvalidCredentials(w)
			return
		}
		authManager.RecordLoginAttempt(clientIP, true)

		session, err := authManager.CreateSession("user")
		if err != nil {
			auditAuth("login_failed", clientIP, "session_creation")
			jsonError(w, "セッション作成に失敗しました", http.StatusInternalServerError)
			return
		}
		authManager.SessionManager().SetSessionCookie(w, r, session)
		auditAuth("login_succeeded", clientIP, "")
		writeAuthenticatedResponse(w, session, "ログインしました")
	}
}

func handleAuthReauthenticate(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		current, ok := middleware.SessionFromContext(r.Context())
		if !ok {
			jsonError(w, "認証が必要です", http.StatusUnauthorized)
			return
		}
		request, ok := decodeAuthRequest(w, r)
		if !ok {
			return
		}
		clientIP := middleware.ClientIPFromRequest(r)
		if !reserveAuthAttempt(w, authManager, clientIP, "reauth") {
			return
		}
		valid, busy := authManager.VerifyCredentials(request.Password, request.TOTPCode)
		if busy {
			auditAuth("reauth_busy", clientIP, "")
			writeAuthRateLimited(w, time.Second)
			return
		}
		if !valid {
			authManager.RecordLoginAttempt(clientIP, false)
			auditAuth("reauth_failed", clientIP, "invalid_credentials")
			writeInvalidCredentials(w)
			return
		}
		authManager.RecordLoginAttempt(clientIP, true)

		rotated, err := authManager.SessionManager().RotateAfterReauthentication(current.ID)
		if err != nil {
			auditAuth("reauth_failed", clientIP, "session_rotation")
			jsonError(w, "再認証に失敗しました", http.StatusUnauthorized)
			return
		}
		authManager.SessionManager().SetSessionCookie(w, r, rotated)
		auditAuth("reauth_succeeded", clientIP, "")
		writeAuthenticatedResponse(w, rotated, "再認証しました")
	}
}

func handleAuthLogout(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if session, ok := middleware.SessionFromContext(r.Context()); ok {
			authManager.SessionManager().DeleteSession(session.ID)
		}
		authManager.SessionManager().ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		auditAuth("logout", middleware.ClientIPFromRequest(r), "")
		jsonResponse(w, map[string]interface{}{
			"success": true,
			"message": "ログアウトしました",
		}, http.StatusOK)
	}
}

func handleAuthLogoutAll(authManager *middleware.AuthSessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok {
			jsonError(w, "認証が必要です", http.StatusUnauthorized)
			return
		}
		deleted := authManager.SessionManager().DeleteAllSessions(session.Username)
		authManager.SessionManager().ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		auditAuth("logout_all", middleware.ClientIPFromRequest(r), "")
		jsonResponse(w, map[string]interface{}{
			"success":          true,
			"deleted_sessions": deleted,
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
			jsonResponse(w, map[string]interface{}{
				"authenticated": false,
				"totp_required": authManager.TOTPRequired(),
			}, http.StatusOK)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"authenticated": true,
			"username":      session.Username,
			"expires_at":    session.ExpiresAt.Format(time.RFC3339),
			"csrf_token":    session.CSRFToken,
			"totp_required": authManager.TOTPRequired(),
		}, http.StatusOK)
	}
}

func decodeAuthRequest(w http.ResponseWriter, r *http.Request) (authRequest, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		jsonError(w, "Content-Type は application/json にしてください", http.StatusUnsupportedMediaType)
		return authRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			jsonError(w, "認証リクエストが大きすぎます", http.StatusRequestEntityTooLarge)
		} else {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		}
		return authRequest{}, false
	}
	if err := rejectDuplicateTopLevelKeys(body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return authRequest{}, false
	}

	var request authRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return authRequest{}, false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return authRequest{}, false
	}
	if request.Password == "" || len(request.Password) > maxPasswordBytes || len(request.TOTPCode) > 32 {
		writeInvalidCredentials(w)
		return authRequest{}, false
	}
	return request, true
}

func rejectDuplicateTopLevelKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errors.New("authentication request must be an object")
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid object key")
		}
		// encoding/json matches struct fields case-insensitively. Require the
		// wire contract's exact spellings so aliases such as "Password" cannot
		// coexist with "password" and create last-value-wins ambiguity.
		if key != "password" && key != "totp_code" {
			return errors.New("unknown authentication field")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate object key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func reserveAuthAttempt(w http.ResponseWriter, authManager *middleware.AuthSessionManager, clientIP, event string) bool {
	allowed, retryAfter := authManager.ReserveAuthAttempt(clientIP)
	if allowed {
		return true
	}
	auditAuth(event+"_rate_limited", clientIP, "")
	writeAuthRateLimited(w, retryAfter)
	return false
}

func writeAuthRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	jsonResponse(w, map[string]interface{}{
		"error":               "認証試行が多すぎます。しばらくしてから再試行してください",
		"retry_after_seconds": seconds,
	}, http.StatusTooManyRequests)
}

func writeInvalidCredentials(w http.ResponseWriter) {
	jsonResponse(w, map[string]string{
		"error": "認証情報を確認してください",
	}, http.StatusUnauthorized)
}

func writeAuthenticatedResponse(w http.ResponseWriter, session *middleware.Session, message string) {
	jsonResponse(w, map[string]interface{}{
		"authenticated": true,
		"message":       message,
		"expires_at":    session.ExpiresAt.Format(time.RFC3339),
		"csrf_token":    session.CSRFToken,
	}, http.StatusOK)
}

func auditAuth(event, clientIP, reason string) {
	clientIP = strings.ReplaceAll(strings.ReplaceAll(clientIP, "\n", ""), "\r", "")
	reason = strings.ReplaceAll(strings.ReplaceAll(reason, "\n", ""), "\r", "")
	log.Printf("security_event=%s client_ip=%q reason=%q", event, clientIP, reason)
}
