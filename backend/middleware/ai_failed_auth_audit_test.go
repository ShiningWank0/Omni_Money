package middleware

import (
	"fmt"
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni_money/backend/audithmac"
)

type aiAuditTestClock struct {
	mu      sync.Mutex
	current time.Time
}

func (clock *aiAuditTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.current
}

func (clock *aiAuditTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.current = clock.current.Add(duration)
	clock.mu.Unlock()
}

func TestAIFailedAuthAuditAggregationKeepsHTTPRateLimitSemantics(t *testing.T) {
	const blockedRequests = 10_000
	const sensitiveToken = "0123456789abcdef0123456789abcdef0123456789A"
	clock := &aiAuditTestClock{current: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	auditStore := newAIAuditTestStore(t)
	var recordsMu sync.Mutex
	var records []aiAuditRecord
	handler := newAIAPIMiddleware(
		nil,
		auditStore,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next handler reached") }),
		clock.Now,
		func(record aiAuditRecord) {
			recordsMu.Lock()
			records = append(records, record)
			recordsMu.Unlock()
		},
	)

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", bytes.NewBufferString("private-body"))
		req.RemoteAddr = "192.0.2.10:12345"
		req.Header.Set("Authorization", "Bearer "+sensitiveToken)
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	for index := 0; index < aiFailedAuthRequestsPerMinute; index++ {
		if recorder := request(); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("request %d status=%d, want 401", index+1, recorder.Code)
		}
	}
	for index := 0; index < blockedRequests; index++ {
		recorder := request()
		if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "60" {
			t.Fatalf("blocked request %d status=%d retry=%q", index+1, recorder.Code, recorder.Header().Get("Retry-After"))
		}
	}

	recordsMu.Lock()
	if len(records) != 2 {
		t.Fatalf("same-window audit records=%d, want 2", len(records))
	}
	if records[0].Reason != "authentication_failed" || records[1].Reason != "authentication_rate_limited" {
		t.Fatalf("first audit records=%#v", records)
	}
	recordsMu.Unlock()

	clock.Advance(time.Minute)
	if recorder := request(); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("post-window status=%d, want 401", recorder.Code)
	}

	recordsMu.Lock()
	defer recordsMu.Unlock()
	if len(records) != 5 {
		t.Fatalf("post-window audit records=%d, want 5: %#v", len(records), records)
	}
	assertAIFailedAuthSummary(t, records, "authentication_failed", aiFailedAuthRequestsPerMinute-1)
	assertAIFailedAuthSummary(t, records, "authentication_rate_limited", blockedRequests-1)
	for _, record := range records {
		if record.AccountReference != "" || record.CredentialID != "" {
			t.Fatalf("failed-auth record contains authenticated metadata: %#v", record)
		}
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sensitiveToken, "Authorization", "private-body"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("failed-auth audit leaked %q: %s", secret, encoded)
		}
	}
}

func TestAIFailedAuthAuditAggregatorSeparatesKeysAndPassesOtherAudits(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	aggregator := newAIFailedAuthAuditAggregator()
	records := []aiAuditRecord{
		{Operation: "analysis", RemoteIP: "192.0.2.1", Reason: "authentication_failed", Status: 401},
		{Operation: "transactions.create", RemoteIP: "192.0.2.1", Reason: "authentication_failed", Status: 401},
		{Operation: "analysis", RemoteIP: "192.0.2.2", Reason: "authentication_failed", Status: 401},
		{Operation: "analysis", RemoteIP: "192.0.2.1", Reason: "authentication_rate_limited", Status: 429},
	}
	for _, record := range records {
		if emitted := aggregator.record(record, now); len(emitted) != 1 {
			t.Fatalf("distinct failed-auth key was suppressed: %#v", record)
		}
	}
	// A different client certificate on the same IP is a different audit
	// subject and must retain its own first/summary sequence.
	secondSubject := aiAuditRecord{
		Operation: "analysis", RemoteIP: "192.0.2.1", Reason: "authentication_failed",
		Status: 401, MTLSClientSHA256: "different-client",
	}
	if emitted := aggregator.record(secondSubject, now); len(emitted) != 1 || emitted[0].MTLSClientSHA256 != "different-client" {
		t.Fatalf("separate mTLS subject was not emitted independently: %#v", emitted)
	}
	if emitted := aggregator.record(secondSubject, now); len(emitted) != 0 {
		t.Fatalf("same mTLS subject was not aggregated: %#v", emitted)
	}
	later := now.Add(time.Minute)
	emitted := aggregator.record(secondSubject, later)
	summaryFound := false
	for _, output := range emitted {
		if output.MTLSClientSHA256 == "different-client" && output.Occurrences == 1 {
			summaryFound = true
		}
	}
	if !summaryFound {
		t.Fatalf("mTLS subject summary missing from %#v", emitted)
	}

	for _, reason := range []string{"scope_forbidden", "rate_limited", ""} {
		for iteration := 0; iteration < 100; iteration++ {
			record := aiAuditRecord{Operation: "analysis", Reason: reason, Status: 403}
			if emitted := aggregator.record(record, now); len(emitted) != 1 || emitted[0] != record {
				t.Fatalf("non-target audit was changed: reason=%q emitted=%#v", reason, emitted)
			}
		}
	}
}

func TestAIFailedAuthAuditAggregatorBoundsUniqueSources(t *testing.T) {
	now := time.Date(2026, 8, 9, 2, 0, 0, 0, time.UTC)
	aggregator := newAIFailedAuthAuditAggregator()
	for index := 0; index < aiFailedAuthAuditMaxDetailedWindow; index++ {
		record := aiAuditRecord{
			Operation: "analysis",
			RemoteIP:  "source-" + time.Unix(int64(index), 0).UTC().Format("150405.000000000"),
			Reason:    "authentication_failed",
			Status:    http.StatusUnauthorized,
		}
		if emitted := aggregator.record(record, now); len(emitted) != 1 {
			t.Fatalf("detailed source %d first event emitted=%d", index, len(emitted))
		}
	}

	overflowEmissions := 0
	for index := 0; index < 10_000; index++ {
		record := aiAuditRecord{
			Operation:        "analysis",
			RemoteIP:         "overflow-source-" + time.Unix(int64(index), 0).UTC().Format("150405.000000000"),
			Reason:           "authentication_failed",
			Status:           http.StatusUnauthorized,
			MTLSClientSHA256: "must-not-in-overflow",
		}
		emitted := aggregator.record(record, now)
		overflowEmissions += len(emitted)
		for _, output := range emitted {
			if output.RemoteIP != "" || output.MTLSClientSHA256 != "" {
				t.Fatalf("overflow audit retained high-cardinality source: %#v", output)
			}
		}
	}
	if overflowEmissions != 1 {
		t.Fatalf("overflow emitted %d records, want 1", overflowEmissions)
	}
	if len(aggregator.windows) != aiFailedAuthAuditMaxDetailedWindow {
		t.Fatalf("detailed windows=%d", len(aggregator.windows))
	}
	overflowIndex := aiFailedAuthAuditOverflowIndex("analysis", "authentication_failed")
	if got := aggregator.overflow[overflowIndex].suppressed; got != 9_999 {
		t.Fatalf("overflow suppressed=%d, want 9999", got)
	}
	later := now.Add(time.Minute)
	newRecord := aiAuditRecord{
		Operation: "analysis", RemoteIP: "reusable-source", Reason: "authentication_failed", Status: http.StatusUnauthorized,
	}
	if emitted := aggregator.record(newRecord, later); len(emitted) != 2 {
		t.Fatalf("expiry emissions=%d, want overflow summary plus new first: %#v", len(emitted), emitted)
	}
	if len(aggregator.windows) != 1 {
		t.Fatalf("expired detailed windows were not reclaimed: %d", len(aggregator.windows))
	}
	if !aggregator.overflow[overflowIndex].started.IsZero() {
		t.Fatal("expired overflow bucket was not reclaimed")
	}
}

func TestAIFailedAuthAuditAggregatorIsRaceSafeAndSaturates(t *testing.T) {
	now := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	aggregator := newAIFailedAuthAuditAggregator()
	record := aiAuditRecord{
		Operation: "analysis",
		RemoteIP:  "192.0.2.20",
		Reason:    "authentication_failed",
		Status:    http.StatusUnauthorized,
	}

	var firstEvents atomic.Int64
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				firstEvents.Add(int64(len(aggregator.record(record, now))))
			}
		}()
	}
	wait.Wait()
	if got := firstEvents.Load(); got != 1 {
		t.Fatalf("concurrent first events=%d, want 1", got)
	}

	window := aiFailedAuthAuditWindow{started: now, suppressed: math.MaxUint64, last: record}
	window = suppressAIFailedAuthAudit(window, record, now)
	if window.suppressed != math.MaxUint64 {
		t.Fatalf("saturating count wrapped to %d", window.suppressed)
	}
}

func TestAIFailedAuthAuditEmitterRunsOutsideAggregatorMutex(t *testing.T) {
	clock := &aiAuditTestClock{current: time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC)}
	auditStore := newAIAuditTestStore(t)
	var handler http.Handler
	var entered atomic.Bool
	done := make(chan struct{})
	emit := func(aiAuditRecord) {
		if !entered.CompareAndSwap(false, true) {
			return
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
		request.RemoteAddr = "198.51.100.2:1000"
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}
	handler = newAIAPIMiddleware(nil, auditStore, http.NotFoundHandler(), clock.Now, emit)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/analysis", nil)
	request.RemoteAddr = "198.51.100.1:1000"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant emitter deadlocked")
	}
}

func newAIAuditTestStore(t *testing.T) *audithmac.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-keyring.json")
	if _, err := audithmac.InitializeFile(path, bytes.NewReader(bytes.Repeat([]byte{0x77}, 32))); err != nil {
		t.Fatal(err)
	}
	store, err := audithmac.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertAIFailedAuthSummary(t *testing.T, records []aiAuditRecord, reason string, occurrences int) {
	t.Helper()
	for _, record := range records {
		if record.Reason != reason || record.Occurrences == 0 {
			continue
		}
		if record.Occurrences != uint64(occurrences) {
			t.Fatalf("%s occurrences=%d, want %d", reason, record.Occurrences, occurrences)
		}
		if record.FirstSeen == "" || record.LastSeen == "" || record.FirstSeen > record.LastSeen {
			t.Fatalf("%s summary timestamps=%q..%q", reason, record.FirstSeen, record.LastSeen)
		}
		return
	}
	t.Fatalf("summary reason=%q occurrences=%d not found in %#v", reason, occurrences, records)
}

func TestAIFailedAuthAuditAggregatorBoundsManyFingerprintsBehindOneIP(t *testing.T) {
	now := time.Date(2026, 8, 9, 5, 0, 0, 0, time.UTC)
	aggregator := newAIFailedAuthAuditAggregator()
	for index := 0; index < aiFailedAuthAuditMaxDetailedWindow+1000; index++ {
		record := aiAuditRecord{
			Operation: "analysis", RemoteIP: "192.0.2.50",
			MTLSClientSHA256: fmt.Sprintf("%064x", index+1),
			Reason: "authentication_failed", Status: http.StatusUnauthorized,
		}
		aggregator.record(record, now)
	}
	if len(aggregator.windows) != aiFailedAuthAuditMaxDetailedWindow {
		t.Fatalf("detailed windows=%d", len(aggregator.windows))
	}
	for _, window := range aggregator.overflow {
		if window.last.MTLSClientSHA256 != "" {
			t.Fatalf("overflow retained fingerprint: %#v", window.last)
		}
	}
}
