package httpjson

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorContractRedactsInternalDetailsAndKeepsLifecycleFlags(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 409, 413, 428, 429, 500, 503, 507} {
		w := httptest.NewRecorder()
		WriteError(w, "private database path /secret/ledger.db", status, map[string]any{"login_required": true, "password": "secret"})
		if w.Code != status || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("invalid wire headers")
		}
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if data["code"] == "" || data["login_required"] != true || data["password"] != nil {
			t.Fatal("invalid error envelope")
		}
		if status >= 500 && strings.Contains(w.Body.String(), "/secret/") {
			t.Fatal("internal detail leaked")
		}
	}
}

func TestWriteSafeErrorPreservesExplicitMessage(t *testing.T) {
	w := httptest.NewRecorder()
	WriteSafeError(w, "ユーザーデータを安全に開けません", 503, map[string]any{"retry_after_seconds": 12})
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data["error"] != "ユーザーデータを安全に開けません" || data["code"] != "service_unavailable" {
		t.Fatalf("safe 5xx envelope = %v", data)
	}
	if data["retry_after_seconds"] != float64(12) {
		t.Fatalf("safe 5xx dropped the retry hint: %v", data)
	}
}

func TestRetryHintSurvivesTheEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, "認証試行が多すぎます", 429, map[string]any{"retry_after_seconds": 5})
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data["error"] != "認証試行が多すぎます" || data["retry_after_seconds"] != float64(5) || data["code"] != "rate_limited" {
		t.Fatalf("429 envelope = %v", data)
	}
}
