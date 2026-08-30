package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"omni_money/backend/database"
	"omni_money/backend/middleware"
)

func TestTagHTTPBoundaryUsesCoreValidationAndSafeStatus(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "tag-api.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)

	request := httptest.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(`{"name":"hidden\u200btag"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe tag status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/tags/999999", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing tag delete status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" {
		t.Fatal("missing consistent JSON error response")
	}
}
