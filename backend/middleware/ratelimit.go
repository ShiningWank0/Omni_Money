// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	globalRateLimitPerMinute   = 120
	loginRateLimitPerMinute    = 10
	aiTxRateLimitPerMinute     = 30
	rateLimitWindow            = time.Minute
	rateLimitRetentionDuration = rateLimitWindow * 2
)

type requestWindow struct {
	Timestamps []time.Time
	LastSeen   time.Time
}

// RateLimiter はIP+バケット単位のスライディングウィンドウ制限
type RateLimiter struct {
	mu          sync.Mutex
	windows     map[string]*requestWindow
	requestSeen uint64
}

// NewRateLimiter はRateLimiterを生成する
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		windows: make(map[string]*requestWindow),
	}
}

// Allow は制限判定を行い、許可可否とヘッダー用情報を返す
func (r *RateLimiter) Allow(key string, limit int, now time.Time) (allowed bool, remaining int, resetAtUnix int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requestSeen++
	if r.requestSeen%200 == 0 {
		r.gc(now)
	}

	win, ok := r.windows[key]
	if !ok {
		win = &requestWindow{}
		r.windows[key] = win
	}

	cutoff := now.Add(-rateLimitWindow)
	filtered := win.Timestamps[:0]
	for _, ts := range win.Timestamps {
		if ts.After(cutoff) {
			filtered = append(filtered, ts)
		}
	}
	win.Timestamps = filtered
	win.LastSeen = now

	if len(win.Timestamps) >= limit {
		reset := win.Timestamps[0].Add(rateLimitWindow)
		return false, 0, reset.Unix()
	}

	win.Timestamps = append(win.Timestamps, now)
	remaining = limit - len(win.Timestamps)
	reset := win.Timestamps[0].Add(rateLimitWindow)
	return true, remaining, reset.Unix()
}

func (r *RateLimiter) gc(now time.Time) {
	cutoff := now.Add(-rateLimitRetentionDuration)
	for key, win := range r.windows {
		if win.LastSeen.Before(cutoff) {
			delete(r.windows, key)
		}
	}
}

func resolveRateLimitBucket(req *http.Request) (bucket string, limit int) {
	path := req.URL.Path
	switch {
	case req.Method == http.MethodPost && path == "/api/auth/login":
		return "login", loginRateLimitPerMinute
	case req.Method == http.MethodPost && path == "/api/v1/ai/transactions":
		return "ai-transactions", aiTxRateLimitPerMinute
	default:
		return "global", globalRateLimitPerMinute
	}
}

// RateLimitMiddleware はAPIにレート制限を適用する
func RateLimitMiddleware(next http.Handler) http.Handler {
	limiter := NewRateLimiter()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/api/v1/ai/") {
			next.ServeHTTP(w, r)
			return
		}

		bucket, limit := resolveRateLimitBucket(r)
		ip := ClientIPFromRequest(r)
		key := ip + "|" + bucket
		allowed, remaining, resetAt := limiter.Allow(key, limit, time.Now())

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))

		if !allowed {
			writeJSONError(w, "リクエストが多すぎます。しばらくしてから再試行してください", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
