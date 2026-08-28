package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
	"omni_money/backend/vault"
)

const (
	maxServerAuthBody       = 16 * 1024
	defaultInvitationAge    = 24 * time.Hour
	defaultPasswordResetAge = 15 * time.Minute
)

type serverBootstrapRequest struct {
	SetupToken     []byte `json:"setup_token_b64"`
	Email          string `json:"email"`
	DisplayName    string `json:"display_name"`
	Password       []byte `json:"password_b64"`
	RecoverySecret []byte `json:"recovery_secret_b64"`
}

func (request *serverBootstrapRequest) clear() {
	clear(request.SetupToken)
	clear(request.Password)
	clear(request.RecoverySecret)
}

type serverInvitationAcceptanceRequest struct {
	Token          []byte `json:"token_b64"`
	DisplayName    string `json:"display_name"`
	Password       []byte `json:"password_b64"`
	RecoverySecret []byte `json:"recovery_secret_b64"`
}

func (request *serverInvitationAcceptanceRequest) clear() {
	clear(request.Token)
	clear(request.Password)
	clear(request.RecoverySecret)
}

type serverLoginRequest struct {
	Email    string `json:"email"`
	Password []byte `json:"password_b64"`
}

func (request *serverLoginRequest) clear() { clear(request.Password) }

type serverPasswordResetCompletionRequest struct {
	Token             []byte `json:"token_b64"`
	RecoverySecret    []byte `json:"recovery_secret_b64"`
	NewPassword       []byte `json:"new_password_b64"`
	NewRecoverySecret []byte `json:"new_recovery_secret_b64"`
}

func (request *serverPasswordResetCompletionRequest) clear() {
	clear(request.Token)
	clear(request.RecoverySecret)
	clear(request.NewPassword)
	clear(request.NewRecoverySecret)
}

type serverReauthenticationRequest struct {
	Password []byte `json:"password_b64"`
}

func (request *serverReauthenticationRequest) clear() { clear(request.Password) }

func handleServerBootstrap(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request serverBootstrapRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		defer request.clear()
		user, err := dependencies.Accounts.Bootstrap(r.Context(), serverauth.BootstrapInput{
			SetupToken: request.SetupToken, Email: request.Email, DisplayName: request.DisplayName,
			Password: request.Password, RecoverySecret: request.RecoverySecret,
		}, dependencies.now())
		if err != nil {
			writeServerAccountError(w, err, serverOperationSetup)
			return
		}
		jsonResponse(w, map[string]interface{}{"user": serverUserResponse(user)}, http.StatusCreated)
	}
}

func handleServerInvitationAcceptance(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request serverInvitationAcceptanceRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		defer request.clear()
		user, err := dependencies.Accounts.AcceptInvitation(r.Context(), serverauth.AcceptInvitationInput{
			Token: string(request.Token), DisplayName: request.DisplayName,
			Password: request.Password, RecoverySecret: request.RecoverySecret,
		}, dependencies.now())
		if err != nil {
			writeServerAccountError(w, err, serverOperationPublicToken)
			return
		}
		jsonResponse(w, map[string]interface{}{"user": serverUserResponse(user)}, http.StatusCreated)
	}
}

func handleServerLogin(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request serverLoginRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		defer request.clear()
		session, err := dependencies.Accounts.Login(r.Context(), request.Email, request.Password, dependencies.now())
		if err != nil {
			auditAuth("server_login_failed", middleware.ClientIPFromRequest(r), "rejected")
			writeServerAccountError(w, err, serverOperationLogin)
			return
		}
		if session == nil || session.UserID == "" {
			auditAuth("server_login_failed", middleware.ClientIPFromRequest(r), "session_creation")
			writeServerAccountError(w, serverauth.ErrServiceUnavailable, serverOperationLogin)
			return
		}
		dependencies.Sessions.SetSessionCookie(w, r, session)
		auditAuth("server_login_succeeded", middleware.ClientIPFromRequest(r), "")
		writeServerAuthenticatedResponse(w, session, "ログインしました")
	}
}

func handleServerReauthentication(dependencies ServerDependencies) http.HandlerFunc {
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
		var request serverReauthenticationRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		defer request.clear()
		if err := dependencies.Accounts.Reauthenticate(r.Context(), session.UserID, request.Password, dependencies.now()); err != nil {
			writeServerAccountError(w, err, serverOperationLogin)
			return
		}
		rotated, err := dependencies.Sessions.RotateAfterReauthentication(session.ID)
		if err != nil {
			dependencies.Sessions.ClearSessionCookie(w, r)
			writeAuthRequired(w)
			return
		}
		dependencies.Sessions.SetSessionCookie(w, r, rotated)
		writeServerAuthenticatedResponse(w, rotated, "再認証しました")
	}
}

func handleServerLogout(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if session, ok := middleware.SessionFromContext(r.Context()); ok {
			dependencies.Sessions.DeleteSession(session.ID)
		}
		dependencies.Sessions.ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		jsonResponse(w, map[string]bool{"success": true}, http.StatusOK)
	}
}

func handleServerLogoutAll(dependencies ServerDependencies) http.HandlerFunc {
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
		deleted := dependencies.Sessions.DeleteAllSessionsForUser(session.UserID)
		dependencies.Sessions.ClearSessionCookie(w, r)
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
		jsonResponse(w, map[string]interface{}{"success": true, "deleted_sessions": deleted}, http.StatusOK)
	}
}

func handleServerAuthStatus(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		bootstrapped, err := dependencies.Control.IsBootstrapped(r.Context())
		if err != nil {
			writeServerAccountError(w, err, serverOperationStatus)
			return
		}
		session, ok := dependencies.Sessions.GetSessionFromRequest(r)
		if !ok || session.UserID == "" {
			jsonResponse(w, map[string]interface{}{
				"authenticated": false, "setup_required": !bootstrapped, "features": serverFeatureResponse(false),
			}, http.StatusOK)
			return
		}
		user, err := dependencies.Control.GetUser(r.Context(), session.UserID)
		if err != nil && !errors.Is(err, control.ErrNotFound) {
			writeServerAccountError(w, err, serverOperationStatus)
			return
		}
		if err != nil || user.State != control.UserActive {
			dependencies.Sessions.DeleteAllSessionsForUser(session.UserID)
			dependencies.Sessions.ClearSessionCookie(w, r)
			jsonResponse(w, map[string]interface{}{
				"authenticated": false, "setup_required": !bootstrapped, "features": serverFeatureResponse(false),
			}, http.StatusOK)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"authenticated": true, "setup_required": false, "user": serverUserResponse(user),
			"expires_at": session.ExpiresAt.Format(time.RFC3339), "csrf_token": session.CSRFToken,
			"idle_timeout_seconds": dependencies.Sessions.IdleTimeoutSeconds(),
			"features":             serverFeatureResponse(user.Role == control.RoleAdmin),
		}, http.StatusOK)
	}
}

func serverFeatureResponse(admin bool) map[string]bool {
	return map[string]bool{
		"admin": admin, "ai": false, "snapshots": false,
	}
}

type serverInvitationCreationRequest struct {
	Email            string       `json:"email"`
	Role             control.Role `json:"role"`
	ExpiresInSeconds int64        `json:"expires_in_seconds,omitempty"`
}

func handleServerInvitationCreation(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		actor, ok := requireServerAdmin(w, r)
		if !ok {
			return
		}
		var request serverInvitationCreationRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		age, ok := boundedRequestedAge(w, request.ExpiresInSeconds, defaultInvitationAge, control.MaxInvitationLifetime)
		if !ok {
			return
		}
		now := dependencies.now()
		invitation, token, err := dependencies.Accounts.CreateInvitation(r.Context(), actor.ID, request.Email, request.Role, now.Add(age), now)
		if err != nil {
			writeServerAccountError(w, err, serverOperationAdmin)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"invitation": serverInvitationResponse(invitation), "token": token,
		}, http.StatusCreated)
	}
}

type serverPasswordResetCreationRequest struct {
	TargetUserID     string `json:"target_user_id"`
	ExpiresInSeconds int64  `json:"expires_in_seconds,omitempty"`
}

func handleServerPasswordResetCreation(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		actor, ok := requireServerAdmin(w, r)
		if !ok {
			return
		}
		var request serverPasswordResetCreationRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		age, ok := boundedRequestedAge(w, request.ExpiresInSeconds, defaultPasswordResetAge, control.MaxPasswordResetLifetime)
		if !ok {
			return
		}
		now := dependencies.now()
		ticket, token, err := dependencies.Accounts.CreatePasswordReset(r.Context(), actor.ID, request.TargetUserID, now.Add(age), now)
		if err != nil {
			writeServerAccountError(w, err, serverOperationAdmin)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"password_reset": serverPasswordResetResponse(ticket), "token": token,
		}, http.StatusCreated)
	}
}

func handleServerPasswordResetCompletion(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var request serverPasswordResetCompletionRequest
		if !decodeStrictServerJSON(w, r, &request) {
			return
		}
		defer request.clear()
		ticket, err := dependencies.Accounts.CompletePasswordReset(r.Context(), serverauth.CompletePasswordResetInput{
			Token: string(request.Token), RecoverySecret: request.RecoverySecret,
			NewPassword: request.NewPassword, NewRecoverySecret: request.NewRecoverySecret,
		}, dependencies.now())
		if err != nil {
			writeServerAccountError(w, err, serverOperationPublicToken)
			return
		}
		jsonResponse(w, map[string]interface{}{"password_reset": serverPasswordResetResponse(ticket)}, http.StatusOK)
	}
}

func handleServerUsers(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, ok := requireServerAdmin(w, r); !ok {
			return
		}
		users, err := dependencies.Control.ListUsers(r.Context())
		if err != nil {
			writeServerAccountError(w, err, serverOperationAdmin)
			return
		}
		result := make([]map[string]interface{}, 0, len(users))
		for _, user := range users {
			result = append(result, serverUserResponse(user))
		}
		jsonResponse(w, map[string]interface{}{"users": result}, http.StatusOK)
	}
}

func handleServerUserAction(dependencies ServerDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		actor, ok := requireServerAdmin(w, r)
		if !ok {
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "disable" {
			http.NotFound(w, r)
			return
		}
		if err := dependencies.Accounts.DisableUser(r.Context(), actor.ID, parts[0], dependencies.now()); err != nil {
			writeServerAccountError(w, err, serverOperationAdmin)
			return
		}
		jsonResponse(w, map[string]bool{"success": true}, http.StatusOK)
	}
}

func requireServerAdmin(w http.ResponseWriter, r *http.Request) (control.UserSummary, bool) {
	user, ok := middleware.AuthenticatedUserFromContext(r.Context())
	if !ok || user.State != control.UserActive || user.Role != control.RoleAdmin {
		jsonError(w, "管理者権限が必要です", http.StatusForbidden)
		return control.UserSummary{}, false
	}
	return user, true
}

func boundedRequestedAge(w http.ResponseWriter, seconds int64, fallback, maximum time.Duration) (time.Duration, bool) {
	if seconds == 0 {
		return fallback, true
	}
	if seconds < 1 || seconds > int64(maximum/time.Second) {
		jsonError(w, "有効期限が無効です", http.StatusBadRequest)
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func decodeStrictServerJSON(w http.ResponseWriter, r *http.Request, destination interface{}) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		jsonError(w, "Content-Type は application/json にしてください", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServerAuthBody)
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

func rejectDuplicateServerJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanServerJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanServerJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := scanServerJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanServerJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

type serverOperation uint8

const (
	serverOperationSetup serverOperation = iota
	serverOperationLogin
	serverOperationPublicToken
	serverOperationAdmin
	serverOperationStatus
)

func writeServerAccountError(w http.ResponseWriter, err error, operation serverOperation) {
	switch {
	case errors.Is(err, serverauth.ErrAuthenticationBusy):
		w.Header().Set("Retry-After", "1")
		jsonError(w, "認証処理が混み合っています。少し待って再試行してください", http.StatusTooManyRequests)
	case operation == serverOperationLogin && (errors.Is(err, serverauth.ErrInvalidCredentials) ||
		errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden)):
		writeInvalidCredentials(w)
	case operation == serverOperationSetup && errors.Is(err, serverauth.ErrSetupUnauthorized):
		jsonError(w, "初期設定を実行できません", http.StatusForbidden)
	case operation == serverOperationSetup && errors.Is(err, serverauth.ErrAlreadySetup):
		jsonError(w, "初期設定は完了済みです", http.StatusConflict)
	case operation == serverOperationPublicToken && (errors.Is(err, control.ErrNotFound) ||
		errors.Is(err, control.ErrInvitationInactive) || errors.Is(err, control.ErrInvitationExpired) ||
		errors.Is(err, control.ErrResetTicketInactive) || errors.Is(err, control.ErrResetTicketExpired) ||
		errors.Is(err, control.ErrConflict) || errors.Is(err, control.ErrCredentialConflict) ||
		errors.Is(err, control.ErrRecoveryConflict) || errors.Is(err, control.ErrForbidden) ||
		errors.Is(err, serverauth.ErrInvalidRecovery)):
		jsonError(w, "招待または回復情報を確認してください", http.StatusBadRequest)
	case errors.Is(err, serverauth.ErrInvalidPassword) || errors.Is(err, serverauth.ErrInvalidAccountData):
		jsonError(w, "アカウント情報が無効です", http.StatusBadRequest)
	case operation == serverOperationAdmin && errors.Is(err, control.ErrNotFound):
		jsonError(w, "対象が見つかりません", http.StatusNotFound)
	case operation == serverOperationAdmin && errors.Is(err, control.ErrForbidden):
		jsonError(w, "管理操作は許可されていません", http.StatusForbidden)
	case operation == serverOperationAdmin && (errors.Is(err, control.ErrConflict) ||
		errors.Is(err, control.ErrSelfDisable) || errors.Is(err, control.ErrLastActiveAdmin)):
		jsonError(w, "現在のアカウント状態では操作できません", http.StatusConflict)
	case errors.Is(err, serverauth.ErrServiceUnavailable) || errors.Is(err, control.ErrStoreClosed) ||
		errors.Is(err, vault.ErrClosed) || errors.Is(err, vault.ErrDraining):
		jsonError(w, "アカウントサービスを利用できません", http.StatusServiceUnavailable)
	default:
		log.Printf("security_event=server_account_operation_failed operation=%d", operation)
		jsonError(w, "リクエストを処理できませんでした", http.StatusInternalServerError)
	}
}

func writeServerAuthenticatedResponse(w http.ResponseWriter, session *middleware.Session, message string) {
	jsonResponse(w, map[string]interface{}{
		"authenticated": true, "message": message, "user_id": session.UserID,
		"email": session.Email, "display_name": session.DisplayName, "role": session.Role,
		"expires_at": session.ExpiresAt.Format(time.RFC3339), "csrf_token": session.CSRFToken,
	}, http.StatusOK)
}

func serverUserResponse(user control.UserSummary) map[string]interface{} {
	result := map[string]interface{}{
		"id": user.ID, "email": user.Email, "display_name": user.DisplayName,
		"role": user.Role, "state": user.State, "created_at": user.CreatedAt.Format(time.RFC3339),
		"updated_at": user.UpdatedAt.Format(time.RFC3339),
	}
	if user.LastLoginAt != nil {
		result["last_login_at"] = user.LastLoginAt.Format(time.RFC3339)
	}
	return result
}

func serverInvitationResponse(invitation control.Invitation) map[string]interface{} {
	return map[string]interface{}{
		"id": invitation.ID, "email": invitation.Email, "role": invitation.Role,
		"state": invitation.State, "created_at": invitation.CreatedAt.Format(time.RFC3339),
		"expires_at": invitation.ExpiresAt.Format(time.RFC3339),
	}
}

func serverPasswordResetResponse(ticket control.PasswordResetTicket) map[string]interface{} {
	return map[string]interface{}{
		"id": ticket.ID, "user_id": ticket.UserID, "state": ticket.State,
		"created_at": ticket.CreatedAt.Format(time.RFC3339), "expires_at": ticket.ExpiresAt.Format(time.RFC3339),
	}
}
