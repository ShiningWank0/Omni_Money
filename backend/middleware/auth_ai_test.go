package middleware

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/audithmac"
)

func TestAIAPIMiddlewareAuditsWithoutSecrets(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789A"
	const bodySecret = "private-memo-that-must-not-be-logged"
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "credentials.json")
	document := &aicredentials.File{
		Version: aicredentials.CurrentVersion,
		Credentials: []aicredentials.Credential{{
			ID:                "audit-test",
			TokenSHA256:       aicredentials.HashToken(token),
			NotBefore:         now.Add(-time.Minute),
			ExpiresAt:         now.Add(time.Hour),
			Scopes:            []string{aicredentials.ScopeAnalysisSummary},
			Accounts:          []string{"cash"},
			MaxAnalysisDays:   30,
			MaxResults:        10,
			AnalysisStartDate: "2026-01-01",
			AnalysisEndDate:   "2026-12-31",
		}},
	}
	if err := aicredentials.WriteFileAtomic(path, document); err != nil {
		t.Fatal(err)
	}
	store, err := aicredentials.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := audithmac.InitializeFile(auditPath, bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))); err != nil {
		t.Fatal(err)
	}
	if _, err := audithmac.RotateFile(auditPath, bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)), now, time.Hour); err != nil {
		t.Fatal(err)
	}
	auditStore, err := audithmac.NewStore(auditPath)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })

	handler := AIAPIMiddleware(store, auditStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential, ok := AICredentialFromContext(r.Context())
		if !ok || credential.ID != "audit-test" {
			t.Fatal("authenticated credential missing from context")
		}
		matched, returned := 7, 3
		RecordAIRequestAudit(r.Context(), "private-account", "2026-08-01", "2026-08-09", true, false, &matched, &returned)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", strings.NewReader(`{"memo":"`+bodySecret+`","amount":867530912345}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:12345"
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: []byte("client-certificate")}}}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	audit := logs.String()
	if !strings.Contains(audit, "AI_API_AUDIT") || !strings.Contains(audit, `"credential_id":"audit-test"`) ||
		!strings.Contains(audit, `"mtls_client_sha256"`) || !strings.Contains(audit, `"account_hmac_sha256"`) ||
		!strings.Contains(audit, `"account_hmac_key_id":"ak1-`) ||
		!strings.Contains(audit, `"account_hmac_previous_sha256"`) ||
		!strings.Contains(audit, `"account_hmac_previous_key_id":"ak1-`) ||
		!strings.Contains(audit, `"start_date":"2026-08-01"`) || !strings.Contains(audit, `"matched_count":7`) ||
		!strings.Contains(audit, `"returned_count":3`) {
		t.Fatalf("audit record missing: %q", audit)
	}
	encodedCurrentKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	encodedPreviousKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	for _, secret := range []string{token, bodySecret, "867530912345", "private-account", encodedCurrentKey, encodedPreviousKey} {
		if strings.Contains(audit, secret) {
			t.Fatalf("audit log leaked %q: %s", secret, audit)
		}
	}
}

func TestAIAPIMiddlewareFailsClosedWithoutAuditStore(t *testing.T) {
	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })
	nextCalled := false
	handler := AIAPIMiddleware(nil, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef0123456789A")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || nextCalled {
		t.Fatalf("status=%d nextCalled=%v", recorder.Code, nextCalled)
	}
	if !strings.Contains(logs.String(), `"reason":"audit_key_unavailable"`) || strings.Contains(logs.String(), request.Header.Get("Authorization")) {
		t.Fatalf("unsafe fail-closed audit: %q", logs.String())
	}
}

func TestAccountHMACSurvivesBearerRotationAndCredentialReload(t *testing.T) {
	const firstToken = "0123456789abcdef0123456789abcdef0123456789A"
	const secondToken = "abcdef0123456789abcdef0123456789abcdef01234"
	now := time.Now().UTC()
	credentialPath := filepath.Join(t.TempDir(), "credentials.json")
	document := &aicredentials.File{
		Version: aicredentials.CurrentVersion,
		Credentials: []aicredentials.Credential{{
			ID:                "rotated-credential",
			TokenSHA256:       aicredentials.HashToken(firstToken),
			NotBefore:         now.Add(-time.Minute),
			ExpiresAt:         now.Add(time.Hour),
			Scopes:            []string{aicredentials.ScopeAnalysisSummary},
			Accounts:          []string{"cash"},
			MaxAnalysisDays:   30,
			MaxResults:        10,
			AnalysisStartDate: "2026-01-01",
			AnalysisEndDate:   "2026-12-31",
		}},
	}
	if err := aicredentials.WriteFileAtomic(credentialPath, document); err != nil {
		t.Fatal(err)
	}
	credentialStore, err := aicredentials.NewStore(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := audithmac.InitializeFile(auditPath, bytes.NewReader(bytes.Repeat([]byte{0x62}, 32))); err != nil {
		t.Fatal(err)
	}
	auditStore, err := audithmac.NewStore(auditPath)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })
	handler := AIAPIMiddleware(credentialStore, auditStore, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RecordAIRequestAudit(r.Context(), "cash", "2026-08-09", "2026-08-09", false, false, nil, nil)
		w.WriteHeader(http.StatusOK)
	}))
	request := func(token string) string {
		logs.Reset()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return auditField(t, logs.String(), "account_hmac_sha256")
	}
	firstReference := request(firstToken)
	document.Credentials[0].TokenSHA256 = aicredentials.HashToken(secondToken)
	if err := aicredentials.WriteFileAtomic(credentialPath, document); err != nil {
		t.Fatal(err)
	}
	if err := credentialStore.Reload(); err != nil {
		t.Fatal(err)
	}
	secondReference := request(secondToken)
	if firstReference == "" || firstReference != secondReference {
		t.Fatalf("reference changed across bearer rotation: before=%q after=%q", firstReference, secondReference)
	}
}

func auditField(t *testing.T, record, field string) string {
	t.Helper()
	marker := `"` + field + `":"`
	start := strings.Index(record, marker)
	if start < 0 {
		t.Fatalf("field %q missing from audit %q", field, record)
	}
	start += len(marker)
	end := strings.IndexByte(record[start:], '"')
	if end < 0 {
		t.Fatalf("field %q is unterminated in audit %q", field, record)
	}
	return record[start : start+end]
}
