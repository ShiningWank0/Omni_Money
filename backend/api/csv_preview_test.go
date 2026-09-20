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

const previewAPIPayload = "account,date,item,type,amount,memo\n" +
	"cash,2026-01-01,coffee,expense,100,\n" +
	"cash,2026-01-02,groceries,expense,500,\n"

func TestCSVPreviewEndpointClassifiesWithoutApplying(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "csv-preview-api.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/import_csv?mode=append&preview=1",
		strings.NewReader(previewAPIPayload))
	request.Header.Set("Content-Type", "text/csv")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var preview struct {
		SourceDigest   string `json:"source_digest"`
		TargetDigest   string `json:"target_digest"`
		NewCount       int    `json:"new_count"`
		DuplicateCount int    `json:"duplicate_count"`
		ConflictCount  int    `json:"conflict_count"`
		ReplaceImpact  *struct {
			Transactions int64 `json:"transactions"`
		} `json:"replace_impact"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.NewCount != 2 || preview.DuplicateCount != 0 || preview.ConflictCount != 0 {
		t.Fatalf("preview classification = %+v", preview)
	}
	if preview.ReplaceImpact != nil {
		t.Fatal("append preview must not carry a replace impact")
	}
	if len(preview.SourceDigest) != 64 || len(preview.TargetDigest) != 64 {
		t.Fatalf("digest lengths = %d/%d", len(preview.SourceDigest), len(preview.TargetDigest))
	}

	// Apply with the pinned digests; the stored ledger must not change during
	// the preview and the pins must still validate.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/api/import_csv?mode=append&preview_source_digest="+preview.SourceDigest+"&preview_target_digest="+preview.TargetDigest,
		strings.NewReader(previewAPIPayload))
	request.Header.Set("Content-Type", "text/csv")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply status = %d; body=%s", recorder.Code, recorder.Body.String())
	}

	// A second preview + a ledger mutation must turn the stale pin into 409.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/import_csv?mode=append&preview=1",
		strings.NewReader(previewAPIPayload))
	request.Header.Set("Content-Type", "text/csv")
	handler.ServeHTTP(recorder, request)
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	create, err := json.Marshal(map[string]interface{}{
		"account": "cash", "date": "2026-03-01", "item": "late", "type": "income", "amount": 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/transactions", strings.NewReader(string(create)))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("transaction create status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/api/import_csv?mode=append&preview_source_digest="+preview.SourceDigest+"&preview_target_digest="+preview.TargetDigest,
		strings.NewReader(previewAPIPayload))
	request.Header.Set("Content-Type", "text/csv")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale pin status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCSVPreviewEndpointRejectsInvalidPins(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "csv-preview-api-invalid.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost,
		"/api/import_csv?mode=append&preview_source_digest=not-hex&preview_target_digest=not-hex",
		strings.NewReader(previewAPIPayload))
	request.Header.Set("Content-Type", "text/csv")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid pin status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCSVPreviewEndpointKeepsJSONPathPinFree(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "csv-preview-api-json.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	mux := http.NewServeMux()
	registerFinancialRoutes(mux)
	handler := middleware.LegacyCoreServiceMiddleware(mux)

	envelope, err := json.Marshal(map[string]string{"content": previewAPIPayload, "mode": "append"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost,
		"/api/import_csv?mode=append&preview=1",
		strings.NewReader(string(envelope)))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("json preview status = %d; body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/api/import_csv?mode=append&preview_source_digest="+strings.Repeat("a", 64)+"&preview_target_digest="+strings.Repeat("b", 64),
		strings.NewReader(string(envelope)))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("json pin status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
}
