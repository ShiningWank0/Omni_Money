package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

func TestTagHTTPDeleteImpactEndpointReportsCascadeCounts(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "tag-impact-api.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)
	create := func(body string) map[string]interface{} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(body))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("tag create status = %d; body=%s", recorder.Code, recorder.Body.String())
		}
		var result map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	root := create(`{"name":"root"}`)
	rootID := int64(root["id"].(float64))
	create(`{"name":"child","parent_id":` + strconv.FormatInt(rootID, 10) + `}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/tags/"+strconv.FormatInt(rootID, 10)+"/impact", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("impact status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"descendant_count":1`) {
		t.Fatalf("impact response = %s", recorder.Body.String())
	}
}
