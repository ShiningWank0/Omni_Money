package middleware

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
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

	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })

	handler := AIAPIMiddleware(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		!strings.Contains(audit, `"start_date":"2026-08-01"`) || !strings.Contains(audit, `"matched_count":7`) ||
		!strings.Contains(audit, `"returned_count":3`) {
		t.Fatalf("audit record missing: %q", audit)
	}
	for _, secret := range []string{token, bodySecret, "867530912345", "private-account"} {
		if strings.Contains(audit, secret) {
			t.Fatalf("audit log leaked %q: %s", secret, audit)
		}
	}
}
