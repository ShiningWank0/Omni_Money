package middleware

import (
	"math"
	"sync"
	"time"
)

const (
	aiFailedAuthAuditWindowDuration    = time.Minute
	aiFailedAuthAuditMaxDetailedWindow = 4096
	aiFailedAuthAuditOperationCount    = 3
	aiFailedAuthAuditReasonCount       = 2
)

type aiNowFunc func() time.Time

type aiAuditLogFunc func(aiAuditRecord)

type aiFailedAuthAuditKey struct {
	remoteIP        string
	mtlsFingerprint string
	operation       string
	reason    string
	status    int
}

type aiFailedAuthAuditWindow struct {
	started         time.Time
	suppressedFirst time.Time
	lastSeen        time.Time
	suppressed      uint64
	last            aiAuditRecord
}

// aiFailedAuthAuditAggregator bounds failed-auth audit cardinality separately
// from the HTTP rate limiter. It never changes whether a request is allowed.
type aiFailedAuthAuditAggregator struct {
	mu        sync.Mutex
	windows   map[aiFailedAuthAuditKey]aiFailedAuthAuditWindow
	overflow  [aiFailedAuthAuditOperationCount * aiFailedAuthAuditReasonCount]aiFailedAuthAuditWindow
	lastSweep time.Time
}

func newAIFailedAuthAuditAggregator() *aiFailedAuthAuditAggregator {
	return &aiFailedAuthAuditAggregator{
		windows: make(map[aiFailedAuthAuditKey]aiFailedAuthAuditWindow),
	}
}

// record returns immutable records that the caller must emit after this method
// releases its mutex. Non-failed-auth audit records pass through unchanged.
func (aggregator *aiFailedAuthAuditAggregator) record(record aiAuditRecord, now time.Time) []aiAuditRecord {
	if aggregator == nil || !isAggregatedFailedAuthReason(record.Reason) {
		return []aiAuditRecord{record}
	}

	aggregator.mu.Lock()
	defer aggregator.mu.Unlock()

	emissions := aggregator.sweepExpiredLocked(now)
	key := aiFailedAuthAuditKey{
		remoteIP:        record.RemoteIP,
		mtlsFingerprint: record.MTLSClientSHA256,
		operation:       normalizeAIAuditOperation(record.Operation),
		reason:    record.Reason,
		status:    record.Status,
	}
	record.Operation = key.operation

	if window, exists := aggregator.windows[key]; exists {
		if !aiFailedAuthAuditWindowExpired(window, now) {
			aggregator.windows[key] = suppressAIFailedAuthAudit(window, record, now)
			return emissions
		}
		if summary, ok := summarizeAIFailedAuthAudit(window, now); ok {
			emissions = append(emissions, summary)
		}
		delete(aggregator.windows, key)
	}

	if len(aggregator.windows) >= aiFailedAuthAuditMaxDetailedWindow {
		return append(emissions, aggregator.recordOverflowLocked(record, now)...)
	}

	aggregator.windows[key] = aiFailedAuthAuditWindow{
		started:  now,
		lastSeen: now,
		last:     record,
	}
	return append(emissions, record)
}

func (aggregator *aiFailedAuthAuditAggregator) sweepExpiredLocked(now time.Time) []aiAuditRecord {
	if !aggregator.lastSweep.IsZero() && now.Sub(aggregator.lastSweep) < aiFailedAuthAuditWindowDuration {
		return nil
	}
	aggregator.lastSweep = now
	return aggregator.sweepAllExpiredLocked(now)
}

func (aggregator *aiFailedAuthAuditAggregator) sweepAllExpiredLocked(now time.Time) []aiAuditRecord {
	var emissions []aiAuditRecord
	for key, window := range aggregator.windows {
		if !aiFailedAuthAuditWindowExpired(window, now) {
			continue
		}
		if summary, ok := summarizeAIFailedAuthAudit(window, now); ok {
			emissions = append(emissions, summary)
		}
		delete(aggregator.windows, key)
	}
	for index := range aggregator.overflow {
		window := aggregator.overflow[index]
		if window.started.IsZero() || !aiFailedAuthAuditWindowExpired(window, now) {
			continue
		}
		if summary, ok := summarizeAIFailedAuthAudit(window, now); ok {
			emissions = append(emissions, summary)
		}
		aggregator.overflow[index] = aiFailedAuthAuditWindow{}
	}
	return emissions
}

func (aggregator *aiFailedAuthAuditAggregator) recordOverflowLocked(record aiAuditRecord, now time.Time) []aiAuditRecord {
	record.RemoteIP = ""
	record.MTLSClientSHA256 = ""
	record.Operation = normalizeAIAuditOperation(record.Operation)
	index := aiFailedAuthAuditOverflowIndex(record.Operation, record.Reason)
	window := aggregator.overflow[index]
	if !window.started.IsZero() && !aiFailedAuthAuditWindowExpired(window, now) {
		aggregator.overflow[index] = suppressAIFailedAuthAudit(window, record, now)
		return nil
	}

	var emissions []aiAuditRecord
	if !window.started.IsZero() {
		if summary, ok := summarizeAIFailedAuthAudit(window, now); ok {
			emissions = append(emissions, summary)
		}
	}
	aggregator.overflow[index] = aiFailedAuthAuditWindow{
		started:  now,
		lastSeen: now,
		last:     record,
	}
	return append(emissions, record)
}

func suppressAIFailedAuthAudit(window aiFailedAuthAuditWindow, record aiAuditRecord, now time.Time) aiFailedAuthAuditWindow {
	if window.suppressed == 0 {
		window.suppressedFirst = now
	}
	if window.suppressed < math.MaxUint64 {
		window.suppressed++
	}
	window.lastSeen = now
	window.last = record
	return window
}

func summarizeAIFailedAuthAudit(window aiFailedAuthAuditWindow, emittedAt time.Time) (aiAuditRecord, bool) {
	if window.suppressed == 0 {
		return aiAuditRecord{}, false
	}
	record := window.last
	record.Timestamp = emittedAt.UTC().Format(time.RFC3339Nano)
	record.Occurrences = window.suppressed
	record.FirstSeen = window.suppressedFirst.UTC().Format(time.RFC3339Nano)
	record.LastSeen = window.lastSeen.UTC().Format(time.RFC3339Nano)
	return record, true
}

func aiFailedAuthAuditWindowExpired(window aiFailedAuthAuditWindow, now time.Time) bool {
	return !window.started.IsZero() && now.Sub(window.started) >= aiFailedAuthAuditWindowDuration
}

func isAggregatedFailedAuthReason(reason string) bool {
	return reason == "authentication_failed" || reason == "authentication_rate_limited"
}

func normalizeAIAuditOperation(operation string) string {
	switch operation {
	case "transactions.create", "analysis":
		return operation
	default:
		return "unknown"
	}
}

func aiFailedAuthAuditOverflowIndex(operation, reason string) int {
	operationIndex := 2
	switch normalizeAIAuditOperation(operation) {
	case "transactions.create":
		operationIndex = 0
	case "analysis":
		operationIndex = 1
	}
	reasonIndex := 0
	if reason == "authentication_rate_limited" {
		reasonIndex = 1
	}
	return reasonIndex*aiFailedAuthAuditOperationCount + operationIndex
}
