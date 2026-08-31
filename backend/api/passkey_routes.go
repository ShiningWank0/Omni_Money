package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
)

const maxPasskeyRequestBody = 2 * 1024 * 1024

type passkeyLoginBeginRequest struct {
	Email string `json:"email"`
}

type passkeyFinishRequest struct {
	CeremonyID     string          `json:"ceremony_id"`
	CredentialJSON json.RawMessage `json:"credential"`
	PRFResult      []byte          `json:"prf_result_b64"`
}

func (request *passkeyFinishRequest) clear() { clear(request.PRFResult) }

type passkeyRegistrationFinishRequest struct {
	CeremonyID     string          `json:"ceremony_id"`
	Name           string          `json:"name"`
	Password       []byte          `json:"password_b64"`
	CredentialJSON json.RawMessage `json:"credential"`
	PRFResult      []byte          `json:"prf_result_b64"`
}

func (request *passkeyRegistrationFinishRequest) clear() {
	clear(request.Password)
	clear(request.PRFResult)
}

func handlePasskeyLoginBegin(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request passkeyLoginBeginRequest
		if !decodeStrictServerJSONLimit(w, r, &request, maxServerAuthBody) {
			return
		}
		result, err := passkeys.BeginPasskeyLogin(r.Context(), request.Email, middleware.ClientIPFromRequest(r))
		if err != nil {
			auditAuth("server_passkey_login_failed", middleware.ClientIPFromRequest(r), "begin_rejected")
			writePasskeyError(w, err, true)
			return
		}
		jsonResponse(w, result, http.StatusOK)
	}
}

func handlePasskeyLoginFinish(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request passkeyFinishRequest
		if !decodeStrictServerJSONLimit(w, r, &request, maxPasskeyRequestBody) {
			return
		}
		defer request.clear()
		session, err := passkeys.FinishPasskeyLogin(r.Context(), serverauth.FinishPasskeyLoginInput{
			CeremonyID: request.CeremonyID, ClientKey: middleware.ClientIPFromRequest(r),
			CredentialJSON: request.CredentialJSON, PRFResult: request.PRFResult,
		}, dependencies.now())
		if err != nil {
			auditAuth("server_passkey_login_failed", middleware.ClientIPFromRequest(r), "finish_rejected")
			writePasskeyError(w, err, true)
			return
		}
		dependencies.Sessions.SetSessionCookie(w, r, session)
		auditAuth("server_passkey_login_succeeded", middleware.ClientIPFromRequest(r), "")
		writeServerAuthenticatedResponse(w, session, "パスキーでログインしました")
	}
}

func handlePasskeyRegistrationBegin(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		result, err := passkeys.BeginPasskeyRegistration(r.Context(), session.UserID, middleware.ClientIPFromRequest(r))
		if err != nil {
			writePasskeyError(w, err, false)
			return
		}
		jsonResponse(w, result, http.StatusOK)
	}
}

func handlePasskeyRegistrationFinish(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		var request passkeyRegistrationFinishRequest
		if !decodeStrictServerJSONLimit(w, r, &request, maxPasskeyRequestBody) {
			return
		}
		defer request.clear()
		result, err := passkeys.FinishPasskeyRegistration(r.Context(), session.UserID, serverauth.FinishPasskeyRegistrationInput{
			CeremonyID: request.CeremonyID, ClientKey: middleware.ClientIPFromRequest(r), Name: request.Name,
			Password: request.Password, CredentialJSON: request.CredentialJSON, PRFResult: request.PRFResult,
		}, dependencies.now())
		if err != nil {
			writePasskeyError(w, err, false)
			return
		}
		jsonResponse(w, map[string]any{"passkey": result}, http.StatusCreated)
	}
}

func handlePasskeyReauthenticationBegin(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		result, err := passkeys.BeginPasskeyReauthentication(r.Context(), session.UserID, middleware.ClientIPFromRequest(r))
		if err != nil {
			writePasskeyError(w, err, false)
			return
		}
		jsonResponse(w, result, http.StatusOK)
	}
}

func handlePasskeyReauthenticationFinish(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		var request passkeyFinishRequest
		if !decodeStrictServerJSONLimit(w, r, &request, maxPasskeyRequestBody) {
			return
		}
		defer request.clear()
		if err := passkeys.FinishPasskeyReauthentication(r.Context(), session.UserID, serverauth.FinishPasskeyLoginInput{
			CeremonyID: request.CeremonyID, ClientKey: middleware.ClientIPFromRequest(r),
			CredentialJSON: request.CredentialJSON, PRFResult: request.PRFResult,
		}, dependencies.now()); err != nil {
			auditAuth("server_passkey_reauthentication_failed", middleware.ClientIPFromRequest(r), "rejected")
			writePasskeyError(w, err, false)
			return
		}
		rotated, err := dependencies.Sessions.RotateAfterReauthentication(session.ID)
		if err != nil {
			dependencies.Sessions.ClearSessionCookie(w, r)
			writeAuthRequired(w)
			return
		}
		dependencies.Sessions.SetSessionCookie(w, r, rotated)
		auditAuth("server_passkey_reauthentication_succeeded", middleware.ClientIPFromRequest(r), "")
		writeServerAuthenticatedResponse(w, rotated, "パスキーで再認証しました")
	}
}

func handlePasskeyList(passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		items, err := passkeys.ListPasskeys(r.Context(), session.UserID)
		if err != nil {
			writePasskeyError(w, err, false)
			return
		}
		jsonResponse(w, map[string]any{"passkeys": items}, http.StatusOK)
	}
}

func handlePasskeyDelete(dependencies ServerDependencies, passkeys ServerPasskeyService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		middleware.ReleaseRequestVaultLease(r.Context())
		encodedID := strings.TrimPrefix(r.URL.Path, "/api/auth/passkeys/")
		if encodedID == "" || strings.Contains(encodedID, "/") {
			jsonError(w, "パスキーIDが無効です", http.StatusBadRequest)
			return
		}
		credentialID, err := base64.RawURLEncoding.DecodeString(encodedID)
		if err != nil {
			jsonError(w, "パスキーIDが無効です", http.StatusBadRequest)
			return
		}
		defer clear(credentialID)
		if !revalidateServerRecentAuth(w, r) {
			return
		}
		if err := passkeys.DeletePasskey(r.Context(), session.UserID, credentialID); err != nil {
			writePasskeyError(w, err, false)
			return
		}
		dependencies.Sessions.ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		// Revocation invalidates every session and drains the open vault. The
		// client must authenticate again so no session derived from the revoked
		// credential remains usable.
		jsonResponse(w, map[string]interface{}{"success": true, "reauthentication_required": true}, http.StatusOK)
	}
}

func handleAllPasskeysDelete(dependencies ServerDependencies, passkeys ServerPasskeyBulkService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		session, ok := middleware.SessionFromContext(r.Context())
		if !ok || session.UserID == "" {
			writeAuthRequired(w)
			return
		}
		middleware.ReleaseRequestVaultLease(r.Context())
		if !revalidateServerRecentAuth(w, r) {
			return
		}
		removed, err := passkeys.DeleteAllPasskeys(r.Context(), session.UserID)
		if err != nil {
			writePasskeyError(w, err, false)
			return
		}
		dependencies.Sessions.ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		jsonResponse(w, map[string]interface{}{"success": true, "revoked_passkeys": removed, "reauthentication_required": true}, http.StatusOK)
	}
}

func writePasskeyError(w http.ResponseWriter, err error, login bool) {
	switch {
	case login || errors.Is(err, serverauth.ErrInvalidCredentials):
		writeInvalidCredentials(w)
	case errors.Is(err, serverauth.ErrPasskeyPRFRequired):
		jsonError(w, "このパスキーはVault暗号化に必要なPRF機能へ対応していません", http.StatusBadRequest)
	case errors.Is(err, serverauth.ErrPasskeyCeremony):
		jsonError(w, "パスキー操作の有効期限が切れました。最初からやり直してください", http.StatusBadRequest)
	case errors.Is(err, control.ErrNotFound):
		jsonError(w, "パスキーが見つかりません", http.StatusNotFound)
	case errors.Is(err, control.ErrConflict):
		jsonError(w, "このパスキーは登録済みか、登録上限に達しています", http.StatusConflict)
	case errors.Is(err, serverauth.ErrPasskeysUnavailable), errors.Is(err, serverauth.ErrServiceUnavailable):
		jsonError(w, "パスキーを利用できません", http.StatusServiceUnavailable)
	default:
		writeServerAccountError(w, err, serverOperationStatus)
	}
}

func decodeStrictServerJSONLimit(w http.ResponseWriter, r *http.Request, destination any, limit int64) bool {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Body == nil || mediaErr != nil || mediaType != "application/json" {
		jsonError(w, "Content-Type は application/json にしてください", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			jsonError(w, "リクエストが大きすぎます", http.StatusRequestEntityTooLarge)
		} else {
			jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		}
		return false
	}
	defer clear(body)
	if err := rejectDuplicateServerJSONKeys(body); err != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || ensureJSONEOF(decoder) != nil {
		jsonError(w, "リクエストデータが無効です", http.StatusBadRequest)
		return false
	}
	return true
}
