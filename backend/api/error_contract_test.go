package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONResponseSerializationFailureIsSafe500(t *testing.T) {
	w := httptest.NewRecorder()
	jsonResponse(w, make(chan int), http.StatusOK)
	if w.Code != 500 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "chan") {
		t.Fatal("serialization error leaked or succeeded")
	}
}

func decodeErrorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, recorder.Body.String())
	}
	return body
}

func TestJSONErrorRedacts5xxWhileSafeErrorPreservesDeliberateText(t *testing.T) {
	recorder := httptest.NewRecorder()
	jsonError(recorder, "internal detail /secret/ledger.db", http.StatusServiceUnavailable)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "/secret/") {
		t.Fatalf("jsonError leaked a 5xx detail: %s", recorder.Body.String())
	}
	if got := decodeErrorBody(t, recorder)["error"]; got != "サーバー内部でエラーが発生しました" {
		t.Fatalf("redacted 5xx message = %v", got)
	}

	recorder = httptest.NewRecorder()
	jsonSafeError(recorder, "この機能はサーバーモードではまだ利用できません", http.StatusServiceUnavailable)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("safe 5xx status = %d", recorder.Code)
	}
	body := decodeErrorBody(t, recorder)
	if body["error"] != "この機能はサーバーモードではまだ利用できません" || body["code"] != "service_unavailable" {
		t.Fatalf("safe 5xx lost its message: %v", body)
	}
}

func TestFeatureUnavailableKeepsItsSpecific503Message(t *testing.T) {
	recorder := httptest.NewRecorder()
	handleServerFeatureUnavailable(recorder, httptest.NewRequest(http.MethodGet, "/api/snapshots", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "この機能はサーバーモードではまだ利用できません") {
		t.Fatalf("feature-unavailable 503 = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "ユーザーデータを安全に開けません") {
		t.Fatal("feature-unavailable 503 was overwritten with the vault message")
	}
}

func TestHealthzKeepsStatusBodyOnUnavailable(t *testing.T) {
	// healthz reports control-DB reachability; an errored bootstrap query is
	// the unavailable case (a zero-user database is still healthy).
	handler := newTestServerRouter(t, &fakeServerAccounts{}, &fakeServerControl{bootstrapErr: errors.New("control database unreachable")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodGet, "/healthz", ""))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz status = %d, want 503", recorder.Code)
	}
	body := decodeErrorBody(t, recorder)
	if body["status"] != "unavailable" {
		t.Fatalf("healthz body lost its status field: %v", body)
	}
	if _, isError := body["code"]; isError {
		t.Fatalf("healthz body was replaced by the error envelope: %v", body)
	}
}

func TestRateLimitEnvelopeKeepsRetryHint(t *testing.T) {
	recorder := httptest.NewRecorder()
	jsonResponse(recorder, map[string]interface{}{
		"error":               "認証試行が多すぎます。しばらくしてから再試行してください",
		"retry_after_seconds": 7,
	}, http.StatusTooManyRequests)
	body := decodeErrorBody(t, recorder)
	if body["retry_after_seconds"] != float64(7) {
		t.Fatalf("retry hint dropped from the 429 envelope: %v", body)
	}
	if body["error"] != "認証試行が多すぎます。しばらくしてから再試行してください" {
		t.Fatalf("4xx message must not be redacted: %v", body)
	}
}
