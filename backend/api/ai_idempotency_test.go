package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/database"
	"omni_money/backend/models"
)

type synchronizedAuditBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedAuditBuffer) Write(content []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(content)
}

func (buffer *synchronizedAuditBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestAIIdempotencyKeyHashStrictValidation(t *testing.T) {
	valid := http.Header{"Idempotency-Key": []string{"request_20260809:0001"}}
	got, err := aiIdempotencyKeyHash(valid)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("request_20260809:0001"))
	if got != want {
		t.Fatalf("hash = %x, want %x", got, want)
	}

	invalid := []http.Header{
		{},
		{"Idempotency-Key": []string{"short"}},
		{"Idempotency-Key": []string{strings.Repeat("a", maxAIIdempotencyKeyBytes+1)}},
		{"Idempotency-Key": []string{"contains whitespace"}},
		{"Idempotency-Key": []string{"request_20260809:0001", "request_20260809:0002"}},
	}
	for _, header := range invalid {
		if _, err := aiIdempotencyKeyHash(header); err == nil {
			t.Fatalf("accepted invalid header %#v", header)
		}
	}
}

func TestCanonicalAITransactionDigestUsesNormalizedSemanticFields(t *testing.T) {
	base := models.TransactionRequest{
		Account: "cash",
		Date:    "2026-08-09",
		Time:    "12:34",
		Item:    "food",
		Type:    "expense",
		Amount:  100,
		Memo:    "memo",
		Tags:    []int64{20, 10},
		Images: []models.TransactionImageRequest{{
			Filename: "receipt.png",
			MimeType: "image/png",
			Data:     "cG5n",
		}},
	}
	reorderedTags := base
	reorderedTags.Tags = []int64{10, 20}
	if canonicalAITransactionDigest(base) != canonicalAITransactionDigest(reorderedTags) {
		t.Fatal("semantically unordered tags changed canonical digest")
	}
	normalizedImageMetadata := base
	normalizedImageMetadata.Images = append([]models.TransactionImageRequest(nil), base.Images...)
	normalizedImageMetadata.Images[0].Filename = " receipt.png "
	normalizedImageMetadata.Images[0].MimeType = ""
	if canonicalAITransactionDigest(base) != canonicalAITransactionDigest(normalizedImageMetadata) {
		t.Fatal("equivalent normalized image metadata changed canonical digest")
	}

	changed := base
	changed.Memo = "different"
	if canonicalAITransactionDigest(base) == canonicalAITransactionDigest(changed) {
		t.Fatal("effective memo change did not change digest")
	}
	changed = base
	changed.Images = append([]models.TransactionImageRequest(nil), base.Images...)
	changed.Images[0].Data = "ZGlmZmVyZW50"
	if canonicalAITransactionDigest(base) == canonicalAITransactionDigest(changed) {
		t.Fatal("image data change did not change digest")
	}
}

func TestAITransactionHTTPIdempotencyQuotaAndSecretHandling(t *testing.T) {
	var audit synchronizedAuditBuffer
	originalLogOutput := log.Writer()
	log.SetOutput(&audit)
	t.Cleanup(func() { log.SetOutput(originalLogOutput) })
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "ai-http.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	handler := newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID:                    "quota-writer",
		Scopes:                []string{aicredentials.ScopeTransactionsCreate},
		Accounts:              []string{"cash"},
		MaxAnalysisDays:       30,
		MaxResults:            10,
		MaxTransactionsPerDay: 1,
	})
	today := time.Now().Format("2006-01-02")
	body := fmt.Sprintf(`{"account":"cash","date":%q,"item":"food","type":"expense","amount":100}`, today)
	rawKey := "never-store-this-raw-key-20260809"

	request := func(key, requestBody string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/transactions", strings.NewReader(requestBody))
		req.Header.Set("Authorization", "Bearer "+testAIToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	first := request(rawKey, body)
	if first.Code != http.StatusCreated || first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("first response status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}
	replay := request(rawKey, body)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay response status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body changed:\nfirst=%s\nreplay=%s", first.Body.String(), replay.Body.String())
	}

	conflictingBody := strings.Replace(body, `"amount":100`, `"amount":101`, 1)
	conflict := request(rawKey, conflictingBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	quota := request("another-valid-key-20260809", body)
	if quota.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d body=%s", quota.Code, quota.Body.String())
	}
	if retryAfter, err := strconv.Atoi(quota.Header().Get("Retry-After")); err != nil || retryAfter < 1 || retryAfter > 24*60*60 {
		t.Fatalf("Retry-After = %q", quota.Header().Get("Retry-After"))
	}

	var transactionCount, idempotencyCount, usage int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM ai_transaction_idempotency").Scan(&idempotencyCount); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow("SELECT successful_creates FROM ai_daily_transaction_usage WHERE credential_id = ?", "quota-writer").Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 1 || idempotencyCount != 1 || usage != 1 {
		t.Fatalf("counts transactions=%d idempotency=%d usage=%d", transactionCount, idempotencyCount, usage)
	}
	if !strings.Contains(audit.String(), `"idempotency_replayed":true`) {
		t.Fatalf("replay was not represented in bounded audit metadata: %s", audit.String())
	}
	if strings.Contains(audit.String(), rawKey) {
		t.Fatalf("raw idempotency key leaked to audit: %s", audit.String())
	}

	var storedKeyHash []byte
	if err := database.GetDB().QueryRow("SELECT idempotency_key_sha256 FROM ai_transaction_idempotency").Scan(&storedKeyHash); err != nil {
		t.Fatal(err)
	}
	wantKeyHash := sha256.Sum256([]byte(rawKey))
	if hex.EncodeToString(storedKeyHash) != hex.EncodeToString(wantKeyHash[:]) {
		t.Fatalf("stored key digest = %x, want %x", storedKeyHash, wantKeyHash)
	}

	waitForAPISnapshot(t)
	snapshots, err := database.ListSnapshots("")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count after create/replay/conflict/quota = %d, want 1", len(snapshots))
	}

	if _, err := database.GetDB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	database.CloseDB()
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte(rawKey)) {
			t.Fatalf("raw idempotency key persisted in %s", path)
		}
	}
}

func TestAITransactionRequiresIdempotencyKeyAfterAuthorization(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "required-key.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	handler := newTestAIRouterWithCredential(t, aicredentials.Credential{
		ID:              "writer",
		Scopes:          []string{aicredentials.ScopeTransactionsCreate},
		Accounts:        []string{"cash"},
		MaxAnalysisDays: 30,
		MaxResults:      10,
	})
	body, err := json.Marshal(models.TransactionRequest{
		Account: "cash",
		Date:    time.Now().Format("2006-01-02"),
		Item:    "food",
		Type:    "expense",
		Amount:  100,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/transactions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := database.GetDB().QueryRow("SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("missing-key request created %d transactions", count)
	}
}
