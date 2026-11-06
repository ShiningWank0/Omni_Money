package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

// These tests exercise the selected dependency itself. No listener is opened.
// Keep the version in go.mod rather than duplicating a security floor here.
func TestEchoDefaultRateLimiterRejectsRotatingSpoofedIPs(t *testing.T) {
	e := echo.New()
	store := echomiddleware.NewRateLimiterMemoryStoreWithConfig(echomiddleware.RateLimiterMemoryStoreConfig{
		Rate: 0.000001, Burst: 1,
	})
	e.Use(echomiddleware.RateLimiter(store))
	e.GET("/limited", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	for index, spoofed := range []string{"203.0.113.1", "203.0.113.2"} {
		req := httptest.NewRequest(http.MethodGet, "http://money.example/limited", nil)
		req.RemoteAddr = "198.51.100.10:12345"
		req.Header.Set("X-Forwarded-For", spoofed)
		req.Header.Set("X-Real-IP", spoofed)
		response := httptest.NewRecorder()
		e.ServeHTTP(response, req)
		want := http.StatusNoContent
		if index > 0 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d: status = %d, want %d", index+1, response.Code, want)
		}
	}
}

type echoTestTransport func(*http.Request) (*http.Response, error)

func (f echoTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestEchoDefaultProxyReplacesSpoofedRealIP(t *testing.T) {
	for _, connection := range []string{"", "X-Real-IP"} {
		t.Run("Connection="+connection, func(t *testing.T) {
			e := echo.New()
			upstream, err := url.Parse("http://upstream.invalid")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			e.Use(echomiddleware.ProxyWithConfig(echomiddleware.ProxyConfig{
				Balancer: echomiddleware.NewRoundRobinBalancer([]*echomiddleware.ProxyTarget{{URL: upstream}}),
				Transport: echoTestTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if got := req.Header.Get("X-Real-IP"); got != "198.51.100.10" {
						t.Errorf("upstream IP = %q, want the connection peer", got)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
				}),
			}))
			req := httptest.NewRequest(http.MethodGet, "http://money.example/proxied", nil)
			req.RemoteAddr = "198.51.100.10:12345"
			req.Header.Set("X-Real-IP", "203.0.113.1")
			req.Header.Set("X-Forwarded-For", "203.0.113.2")
			req.Header.Set("Connection", connection)
			response := httptest.NewRecorder()
			e.ServeHTTP(response, req)
			if response.Code != http.StatusOK || calls != 1 {
				t.Fatalf("proxy status = %d, upstream calls = %d", response.Code, calls)
			}
		})
	}
}
