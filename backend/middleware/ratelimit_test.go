package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginRateLimitCannotBeBypassedBySpoofedForwardingHeaders(t *testing.T) {
	for _, test := range []struct {
		name       string
		remoteAddr string
		xff        string
	}{
		{name: "direct peer", remoteAddr: "203.0.113.10:12345", xff: "%s"},
		{name: "trusted proxy chain", remoteAddr: "10.0.0.1:12345", xff: "%s, 203.0.113.10"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &ProxyConfig{trustedCIDRs: parseTrustedProxies("10.0.0.0/24")}
			accepted := 0
			handler := ProxyMiddleware(cfg, RateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accepted++
				if got := ClientIPFromRequest(r); got != "203.0.113.10" {
					t.Errorf("client IP = %q, want the real client", got)
				}
				for _, name := range []string{"X-Real-IP", "X-Forwarded-For"} {
					if r.Header.Get(name) != "" {
						t.Errorf("untrusted %s reached the handler", name)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})))
			for index := 0; index < 2*loginRateLimitPerMinute; index++ {
				spoofedIP := fmt.Sprintf("198.51.100.%d", index+1)
				request := httptest.NewRequest(http.MethodPost, "https://money.example/api/auth/login", nil)
				request.RemoteAddr = test.remoteAddr
				request.Header.Set("X-Forwarded-For", fmt.Sprintf(test.xff, spoofedIP))
				request.Header.Set("X-Real-IP", spoofedIP)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				want := http.StatusNoContent
				if index >= loginRateLimitPerMinute {
					want = http.StatusTooManyRequests
				}
				if response.Code != want {
					t.Fatalf("request %d: status = %d, want %d", index+1, response.Code, want)
				}
			}
			if accepted != loginRateLimitPerMinute {
				t.Fatalf("accepted %d login attempts, want %d", accepted, loginRateLimitPerMinute)
			}
		})
	}
}

func TestRateLimiterBoundsClientKeyMapAndUsesOverflowBucket(t *testing.T) {
	limiter := NewRateLimiterWithMaxEntries(2)
	now := time.Unix(1_700_000_000, 0)

	for _, key := range []string{"one|login", "two|login"} {
		if allowed, _, _ := limiter.Allow(key, 1, now); !allowed {
			t.Fatalf("initial request for %q was denied", key)
		}
	}
	if allowed, _, _ := limiter.Allow("three|login", 1, now); !allowed {
		t.Fatal("first overflow request was denied")
	}
	if allowed, _, _ := limiter.Allow("four|login", 1, now); allowed {
		t.Fatal("overflow bucket did not share the login limit")
	}

	if got := len(limiter.windows); got != 2 {
		t.Fatalf("windows map size = %d, want 2", got)
	}
	if got := len(limiter.overflowWindows); got != 1 {
		t.Fatalf("overflow map size = %d, want 1", got)
	}
}

func TestPasswordVerificationRoutesUseTightRateBuckets(t *testing.T) {
	tests := []struct {
		path       string
		wantBucket string
	}{
		{path: "/api/auth/login", wantBucket: "login"},
		{path: "/api/auth/reauth", wantBucket: "reauth"},
		{path: "/api/auth/passkeys/reauth/begin", wantBucket: "reauth"},
		{path: "/api/auth/passkeys/reauth/finish", wantBucket: "reauth"},
		{path: "/api/auth/setup", wantBucket: "account-auth"},
		{path: "/api/auth/passkeys/register/finish", wantBucket: "account-auth"},
		{path: "/api/auth/passkeys/login/begin", wantBucket: "account-auth"},
		{path: "/api/auth/passkeys/login/finish", wantBucket: "account-auth"},
		{path: "/api/auth/passkeys/discover/begin", wantBucket: "account-auth"},
		{path: "/api/auth/passkeys/discover/finish", wantBucket: "account-auth"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, "https://money.example"+test.path, nil)
		bucket, limit := resolveRateLimitBucket(request)
		if bucket != test.wantBucket || limit != loginRateLimitPerMinute {
			t.Errorf("%s bucket/limit = %q/%d, want %q/%d", test.path, bucket, limit, test.wantBucket, loginRateLimitPerMinute)
		}
	}
}

func TestRateLimiterGarbageCollectsBeforeOverflow(t *testing.T) {
	limiter := NewRateLimiterWithMaxEntries(1)
	now := time.Unix(1_700_000_000, 0)
	if allowed, _, _ := limiter.Allow("old|global", 1, now); !allowed {
		t.Fatal("initial request was denied")
	}
	if allowed, _, _ := limiter.Allow("new|global", 1, now.Add(3*rateLimitWindow)); !allowed {
		t.Fatal("request after retention period was denied")
	}
	if _, ok := limiter.windows["old|global"]; ok {
		t.Fatal("stale window was not garbage collected")
	}
	if _, ok := limiter.windows["new|global"]; !ok {
		t.Fatal("new window was not admitted after garbage collection")
	}
}

func TestRateLimiterRejectsNonPositiveLimitWithoutPanic(t *testing.T) {
	limiter := NewRateLimiter()
	allowed, remaining, reset := limiter.Allow("bad", 0, time.Unix(1_700_000_000, 0))
	if allowed || remaining != 0 || reset == 0 {
		t.Fatalf("unexpected result for non-positive limit: allowed=%v remaining=%d reset=%d", allowed, remaining, reset)
	}
}
