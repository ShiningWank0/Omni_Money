// Package httpjson owns the shared HTTP error wire contract.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// InternalErrorMessage is the generic, non-revealing fallback for unexpected
// server failures. Callers with a deliberately safe, user-meaningful 5xx text
// must use WriteSafeError so the redaction does not erase it.
const InternalErrorMessage = "サーバー内部でエラーが発生しました"

// WriteError emits the error envelope and redacts every 5xx caller message, so
// an accidental err.Error() can never leak internal detail.
func WriteError(w http.ResponseWriter, message string, status int, flags map[string]any) {
	writeError(w, message, status, flags, false)
}

// WriteSafeError emits the error envelope while preserving the caller-provided
// message for any status. The caller is responsible for ensuring the message
// contains no internal detail, credentials, or user data.
func WriteSafeError(w http.ResponseWriter, message string, status int, flags map[string]any) {
	writeError(w, message, status, flags, true)
}

func writeError(w http.ResponseWriter, message string, status int, flags map[string]any, safe bool) {
	code := errorCode(status, flags)
	if status >= http.StatusInternalServerError && !safe {
		message = InternalErrorMessage
	}
	if message == "" {
		if status >= http.StatusInternalServerError {
			message = InternalErrorMessage
		} else {
			message = http.StatusText(status)
		}
	}
	body := map[string]any{"error": message, "code": code}
	for _, key := range []string{"login_required", "recent_auth_required", "csrf_rejected"} {
		if flag, ok := flags[key].(bool); ok {
			body[key] = flag
		}
	}
	// Retry hints are safe scalars and part of the rate-limit contract; the
	// 4xx envelope must not silently drop them.
	switch retry := flags["retry_after_seconds"].(type) {
	case int:
		body["retry_after_seconds"] = retry
	case int64:
		body["retry_after_seconds"] = retry
	case float64:
		if retry >= 0 {
			body["retry_after_seconds"] = int64(retry)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func errorCode(status int, flags map[string]any) string {
	if flags["csrf_rejected"] == true {
		return "csrf_rejected"
	}
	switch status {
	case 400:
		return "invalid_request"
	case 401:
		return "authentication_required"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 405:
		return "method_not_allowed"
	case 409:
		return "conflict"
	case 413:
		return "payload_too_large"
	case 415:
		return "unsupported_media_type"
	case 428:
		return "recent_auth_required"
	case 429:
		return "rate_limited"
	case 503:
		return "service_unavailable"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "request_failed"
	}
}

func NotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, "対象が見つかりません", http.StatusNotFound, nil)
}
