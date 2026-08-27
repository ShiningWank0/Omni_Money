// Package middleware は認証、AI用APIの接続制御を提供する。
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"omni_money/backend/aicredentials"
	"omni_money/backend/audithmac"
)

const (
	maxAuthorizationHeaderBytes    = 1024
	minAITokenBytes                = 43
	maxAITokenBytes                = 512
	aiAnalysisRequestsPerMinute    = 120
	aiTransactionRequestsPerMinute = 30
	aiFailedAuthRequestsPerMinute  = 30
)

type aiCredentialContextKey struct{}
type aiAuditScopeContextKey struct{}

type aiRequestAuditScope struct {
	auditKeys                audithmac.Snapshot
	accountReference         string
	accountReferenceKeyID    string
	previousAccountReference string
	previousAccountKeyID     string
	startDate                string
	endDate                  string
	includeDetails           bool
	includeMemo              bool
	matchedCount             *int
	returnedCount            *int
	idempotencyReplayed      bool
}

// RecordAIIdempotencyReplay marks a successful write as a replay without
// attaching the idempotency key or request content to the audit record.
func RecordAIIdempotencyReplay(ctx context.Context) {
	scope, ok := ctx.Value(aiAuditScopeContextKey{}).(*aiRequestAuditScope)
	if ok && scope != nil {
		scope.idempotencyReplayed = true
	}
}

// AICredentialFromContext returns the authenticated AI credential attached by
// AIAPIMiddleware. The returned value is a defensive copy owned by the request.
func AICredentialFromContext(ctx context.Context) (*aicredentials.Credential, bool) {
	credential, ok := ctx.Value(aiCredentialContextKey{}).(*aicredentials.Credential)
	return credential, ok && credential != nil
}

// RecordAIRequestAudit attaches only bounded, non-content metadata to the
// current AI request's audit event. Account names are pseudonymized with the
// dedicated audit keyring; amounts, item text, memos, and bodies are never stored.
func RecordAIRequestAudit(ctx context.Context, account, startDate, endDate string, includeDetails, includeMemo bool, matchedCount, returnedCount *int) {
	scope, ok := ctx.Value(aiAuditScopeContextKey{}).(*aiRequestAuditScope)
	if !ok || scope == nil {
		return
	}
	references := scope.auditKeys.AccountReferences(account, time.Now().UTC())
	scope.accountReference = references.Current.HMACSHA256
	scope.accountReferenceKeyID = references.Current.KeyID
	scope.previousAccountReference = ""
	scope.previousAccountKeyID = ""
	if references.Previous != nil {
		scope.previousAccountReference = references.Previous.HMACSHA256
		scope.previousAccountKeyID = references.Previous.KeyID
	}
	scope.startDate = startDate
	scope.endDate = endDate
	scope.includeDetails = includeDetails
	scope.includeMemo = includeMemo
	if matchedCount != nil {
		value := *matchedCount
		scope.matchedCount = &value
	}
	if returnedCount != nil {
		value := *returnedCount
		scope.returnedCount = &value
	}
}

type aiRateWindow struct {
	started time.Time
	count   int
}

type aiCredentialRateLimiter struct {
	mu      sync.Mutex
	windows map[string]aiRateWindow
}

func newAICredentialRateLimiter() *aiCredentialRateLimiter {
	return &aiCredentialRateLimiter{windows: make(map[string]aiRateWindow)}
}

func (limiter *aiCredentialRateLimiter) allow(key string, limit int, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if _, exists := limiter.windows[key]; !exists && len(limiter.windows) >= 4096 {
		for candidate, candidateWindow := range limiter.windows {
			if now.Sub(candidateWindow.started) >= time.Minute {
				delete(limiter.windows, candidate)
			}
		}
		if len(limiter.windows) >= 4096 {
			return false
		}
	}

	window := limiter.windows[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		limiter.windows[key] = aiRateWindow{started: now, count: 1}
		return true
	}
	if window.count >= limit {
		return false
	}
	window.count++
	limiter.windows[key] = window

	return true
}

func (limiter *aiCredentialRateLimiter) blocked(key string, limit int, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window, exists := limiter.windows[key]
	if !exists {
		return false
	}
	if now.Sub(window.started) >= time.Minute {
		delete(limiter.windows, key)
		return false
	}
	return window.count >= limit
}

type aiAuditRecord struct {
	Timestamp                     string `json:"timestamp"`
	CredentialID                  string `json:"credential_id,omitempty"`
	Operation                     string `json:"operation"`
	RemoteIP                      string `json:"remote_ip,omitempty"`
	Allowed                       bool   `json:"allowed"`
	Status                        int    `json:"status"`
	DurationMS                    int64  `json:"duration_ms"`
	Reason                        string `json:"reason,omitempty"`
	MTLSClientSHA256              string `json:"mtls_client_sha256,omitempty"`
	AccountReference              string `json:"account_hmac_sha256,omitempty"`
	AccountReferenceKeyID         string `json:"account_hmac_key_id,omitempty"`
	PreviousAccountReference      string `json:"account_hmac_previous_sha256,omitempty"`
	PreviousAccountReferenceKeyID string `json:"account_hmac_previous_key_id,omitempty"`
	StartDate                     string `json:"start_date,omitempty"`
	EndDate                       string `json:"end_date,omitempty"`
	IncludeDetails                bool   `json:"include_details,omitempty"`
	IncludeMemo                   bool   `json:"include_memo,omitempty"`
	MatchedCount                  *int   `json:"matched_count,omitempty"`
	ReturnedCount                 *int   `json:"returned_count,omitempty"`
	IdempotencyReplayed           bool   `json:"idempotency_replayed,omitempty"`
	Occurrences                   uint64 `json:"occurrences,omitempty"`
	FirstSeen                     string `json:"first_seen,omitempty"`
	LastSeen                      string `json:"last_seen,omitempty"`
}

// AIAPIMiddleware authenticates scoped, expiring credentials for the isolated
// AI listener. It never logs bearer tokens, request bodies, memos, or amounts.
func AIAPIMiddleware(store *aicredentials.Store, auditStore *audithmac.Store, next http.Handler) http.Handler {
	return newAIAPIMiddleware(store, auditStore, next, time.Now, writeAIAuditLog)
}

func newAIAPIMiddleware(
	store *aicredentials.Store,
	auditStore *audithmac.Store,
	next http.Handler,
	now aiNowFunc,
	emit aiAuditLogFunc,
) http.Handler {
	if now == nil {
		now = time.Now
	}
	limiter := newAICredentialRateLimiter()
	failedAuthAudits := newAIFailedAuthAuditAggregator()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ai/") {
			next.ServeHTTP(w, r)
			return
		}

		started := now()
		operation := aiOperation(r.URL.Path)
		remoteIP := remoteHost(r.RemoteAddr)
		credentialID := ""
		mtlsClientFingerprint := peerCertificateFingerprint(r)
		var requestScope *aiRequestAuditScope
		audit := func(allowed bool, status int, reason string) {
			completed := now()
			duration := completed.Sub(started).Milliseconds()
			if duration < 0 {
				duration = 0
			}
			record := aiAuditRecord{
				Timestamp:        completed.UTC().Format(time.RFC3339Nano),
				CredentialID:     credentialID,
				Operation:        operation,
				RemoteIP:         remoteIP,
				Allowed:          allowed,
				Status:           status,
				DurationMS:       duration,
				Reason:           reason,
				MTLSClientSHA256: mtlsClientFingerprint,
			}
			if requestScope != nil {
				record.AccountReference = requestScope.accountReference
				record.AccountReferenceKeyID = requestScope.accountReferenceKeyID
				record.PreviousAccountReference = requestScope.previousAccountReference
				record.PreviousAccountReferenceKeyID = requestScope.previousAccountKeyID
				record.StartDate = requestScope.startDate
				record.EndDate = requestScope.endDate
				record.IncludeDetails = requestScope.includeDetails
				record.IncludeMemo = requestScope.includeMemo
				record.MatchedCount = requestScope.matchedCount
				record.ReturnedCount = requestScope.returnedCount
				record.IdempotencyReplayed = requestScope.idempotencyReplayed
			}
			if emit == nil {
				return
			}
			for _, emittedRecord := range failedAuthAudits.record(record, completed) {
				emit(emittedRecord)
			}
		}

		failedAuthenticationKey := "authentication-failed\x00" + remoteIP
		if auditStore == nil || auditStore.CurrentKeyID() == "" {
			writeJSONError(w, "AI監査設定が利用できません", http.StatusServiceUnavailable)
			audit(false, http.StatusServiceUnavailable, "audit_key_unavailable")
			return
		}
		if limiter.blocked(failedAuthenticationKey, aiFailedAuthRequestsPerMinute, now()) {
			w.Header().Set("Retry-After", "60")
			writeJSONError(w, "リクエストが多すぎます", http.StatusTooManyRequests)
			audit(false, http.StatusTooManyRequests, "authentication_rate_limited")
			return
		}
		providedToken, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || store == nil {
			if !limiter.allow(failedAuthenticationKey, aiFailedAuthRequestsPerMinute, now()) {
				w.Header().Set("Retry-After", "60")
				writeJSONError(w, "リクエストが多すぎます", http.StatusTooManyRequests)
				audit(false, http.StatusTooManyRequests, "authentication_rate_limited")
				return
			}
			writeJSONError(w, "認証が必要です", http.StatusUnauthorized)
			audit(false, http.StatusUnauthorized, "authentication_failed")
			return
		}
		credential, err := store.Authenticate(providedToken, now())
		if err != nil {
			if !limiter.allow(failedAuthenticationKey, aiFailedAuthRequestsPerMinute, now()) {
				w.Header().Set("Retry-After", "60")
				writeJSONError(w, "リクエストが多すぎます", http.StatusTooManyRequests)
				audit(false, http.StatusTooManyRequests, "authentication_rate_limited")
				return
			}
			writeJSONError(w, "認証が必要です", http.StatusUnauthorized)
			audit(false, http.StatusUnauthorized, "authentication_failed")
			return
		}
		credentialID = credential.ID
		requestScope = &aiRequestAuditScope{auditKeys: auditStore.Snapshot()}

		if r.Method != http.MethodPost {
			writeJSONError(w, "AI用APIは新規追加(POST)と分析(POST)のみ許可されています", http.StatusForbidden)
			audit(false, http.StatusForbidden, "method_forbidden")
			return
		}
		requiredScope := aiRequiredScope(r.URL.Path)
		if requiredScope == "" {
			writeJSONError(w, "Not Found", http.StatusNotFound)
			audit(false, http.StatusNotFound, "path_not_allowed")
			return
		}
		if !credential.HasScope(requiredScope) {
			writeJSONError(w, "この操作を行う権限がありません", http.StatusForbidden)
			audit(false, http.StatusForbidden, "scope_forbidden")
			return
		}
		if r.Header.Get("X-Omni-AI-Console-Relay") == "1" && !credential.AllowConsoleRelay {
			writeJSONError(w, "管理画面から中継する権限がありません", http.StatusForbidden)
			audit(false, http.StatusForbidden, "console_relay_forbidden")
			return
		}

		if !limiter.allow(credential.ID+"\x00"+remoteIP+"\x00"+operation, aiRequestLimit(r.URL.Path), now()) {
			w.Header().Set("Retry-After", "60")
			writeJSONError(w, "リクエストが多すぎます", http.StatusTooManyRequests)
			audit(false, http.StatusTooManyRequests, "rate_limited")
			return
		}

		recorder := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
		ctx := context.WithValue(r.Context(), aiCredentialContextKey{}, credential)
		ctx = context.WithValue(ctx, aiAuditScopeContextKey{}, requestScope)
		next.ServeHTTP(recorder, r.WithContext(ctx))
		audit(recorder.status < http.StatusBadRequest, recorder.status, "")
	})
}

func writeAIAuditLog(record aiAuditRecord) {
	if encoded, err := json.Marshal(record); err == nil {
		log.Printf("AI_API_AUDIT %s", encoded)
	}
}

func peerCertificateFingerprint(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	digest := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	return hex.EncodeToString(digest[:])
}

func aiRequestLimit(path string) int {
	if path == "/api/v1/ai/transactions" {
		return aiTransactionRequestsPerMinute
	}
	return aiAnalysisRequestsPerMinute
}

func bearerToken(header string) (string, bool) {
	if len(header) > maxAuthorizationHeaderBytes || !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if len(token) < minAITokenBytes || len(token) > maxAITokenBytes || strings.TrimSpace(token) != token {
		return "", false
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", false
		}
	}
	return token, true
}

func aiRequiredScope(path string) string {
	switch path {
	case "/api/v1/ai/transactions":
		return aicredentials.ScopeTransactionsCreate
	case "/api/v1/ai/analysis":
		return aicredentials.ScopeAnalysisSummary
	default:
		return ""
	}
}

func aiOperation(path string) string {
	switch path {
	case "/api/v1/ai/transactions":
		return "transactions.create"
	case "/api/v1/ai/analysis":
		return "analysis"
	default:
		return "unknown"
	}
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

type auditResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// writeJSONError はJSON形式のエラーレスポンスを返す。
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
