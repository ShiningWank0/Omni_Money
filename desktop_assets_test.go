package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesktopIndexMarkerIsLimitedToBindingHostHTML(t *testing.T) {
	middleware, err := newDesktopAssetMiddleware("")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><html lang=ja><head></head><body></body></html>"))
		} else {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write([]byte("export const test = true"))
		}
	})
	for _, path := range []string{"/", "/assets/main.js"} {
		response := httptest.NewRecorder()
		middleware(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://wails.localhost"+path, nil))
		if strings.Contains(response.Body.String(), `data-omni-desktop="wails3"`) != (path == "/") {
			t.Fatal("marker escaped the HTML response boundary")
		}
		if len(response.Result().Cookies()) != 0 {
			t.Fatal("native assets must not create browser diagnostic cookies")
		}
	}
}

func TestWailsBrowserDiagnosticRequiresSameOriginAndProcessCapability(t *testing.T) {
	const origin = "http://127.0.0.1:12345"
	create := func() http.Handler {
		middleware, err := newDesktopAssetMiddleware(origin)
		if err != nil {
			t.Fatal(err)
		}
		return middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><head></head></html>"))
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		}))
	}
	handler := create()
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, origin+"/", nil))
	cookies := first.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("missing protected capability")
	}
	for _, path := range []string{"/wails/runtime", "/wails/events", "/wails/stream/ws"} {
		for _, scenario := range []string{"missing-cookie", "bad-cookie", "missing-origin", "foreign-origin", "valid"} {
			r := httptest.NewRequest(http.MethodPost, origin+path, nil)
			if scenario != "missing-cookie" {
				c := *cookies[0]
				if scenario == "bad-cookie" {
					c.Value = "wrong"
				}
				r.AddCookie(&c)
			}
			if scenario != "missing-origin" {
				value := origin
				if scenario == "foreign-origin" {
					value = "https://attacker.invalid"
				}
				r.Header.Set("Origin", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			expected := http.StatusForbidden
			if scenario == "valid" {
				expected = http.StatusNoContent
			}
			if response.Code != expected {
				t.Fatalf("%s/%s status=%d, want %d", path, scenario, response.Code, expected)
			}
		}
	}
	wrongHost := httptest.NewRecorder()
	handler.ServeHTTP(wrongHost, httptest.NewRequest(http.MethodGet, "http://attacker.invalid/", nil))
	if wrongHost.Code != http.StatusForbidden || len(wrongHost.Result().Cookies()) != 0 {
		t.Fatal("DNS-rebinding host received a capability")
	}
	second := httptest.NewRecorder()
	create().ServeHTTP(second, httptest.NewRequest(http.MethodGet, origin+"/", nil))
	if second.Result().Cookies()[0].Value == cookies[0].Value {
		t.Fatal("a new binding host reused the capability")
	}
}
