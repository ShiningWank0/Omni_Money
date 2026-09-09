// Package httpjson owns the shared HTTP error wire contract.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// WriteError preserves legacy error text and lifecycle flags, while adding
// a stable machine-readable code. HTTP status remains authoritative.
func WriteError(w http.ResponseWriter, message string, status int, flags map[string]any) {
	code := "request_failed"
	switch status {
	case 400:
		code = "invalid_request"
	case 401:
		code = "authentication_required"
	case 403:
		code = "forbidden"
	case 404:
		code = "not_found"
	case 405:
		code = "method_not_allowed"
	case 409:
		code = "conflict"
	case 413:
		code = "payload_too_large"
	case 415:
		code = "unsupported_media_type"
	case 428:
		code = "recent_auth_required"
	case 429:
		code = "rate_limited"
	case 503:
		code = "service_unavailable"
	default:
		if status >= 500 {
			code = "internal_error"
		}
	}
	if flags["csrf_rejected"] == true {
		code = "csrf_rejected"
	}
	if status >= 500 {
		message = "サーバー内部でエラーが発生しました"
	}
	if status == http.StatusServiceUnavailable {
		message = "ユーザーデータを安全に開けません"
	}
	body := map[string]any{"error": message, "code": code}
	for _, key := range []string{"login_required", "recent_auth_required", "csrf_rejected"} {
		if flag, ok := flags[key].(bool); ok {
			body[key] = flag
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func NotFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, "対象が見つかりません", http.StatusNotFound, nil)
}
