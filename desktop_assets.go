package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type desktopIndexResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *desktopIndexResponse) Header() http.Header { return w.header }
func (w *desktopIndexResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *desktopIndexResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(data) > 1<<20 {
		return 0, errors.New("desktop index exceeds limit")
	}
	return w.body.Write(data)
}

// The marker is added by the native binding host, never by the product server
// or frontend build. It prevents the shared server UI from loading native IPC.
func newDesktopAssetMiddleware(browserOrigin string) (func(http.Handler) http.Handler, error) {
	var token, cookieName, host string
	if browserOrigin != "" {
		origin, err := url.Parse(browserOrigin)
		if err != nil || origin.Scheme != "https" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.String() != "https://"+origin.Host {
			return nil, errors.New("invalid diagnostic origin")
		}
		host = origin.Host
		cookieName = "wails_ipc_capability_" + origin.Port()
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		token = base64.RawURLEncoding.EncodeToString(secret)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if browserOrigin != "" {
				if r.Host != host || r.TLS == nil {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				protected := strings.HasPrefix(r.URL.Path, "/wails/runtime") && r.URL.Path != "/wails/runtime.js" ||
					strings.HasPrefix(r.URL.Path, "/wails/events") || strings.HasPrefix(r.URL.Path, "/wails/stream") ||
					strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
				if protected {
					cookie, err := r.Cookie(cookieName)
					if err != nil || r.Header.Get("Origin") != browserOrigin || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
				}
			}
			if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != "/index.html") {
				next.ServeHTTP(w, r)
				return
			}
			request := r.Clone(r.Context())
			request.Header.Del("Accept-Encoding")
			request.Header.Del("If-None-Match")
			request.Header.Del("If-Modified-Since")
			response := &desktopIndexResponse{header: make(http.Header)}
			next.ServeHTTP(response, request)
			body := response.body.Bytes()
			if response.status == http.StatusOK {
				if !strings.Contains(response.header.Get("Content-Type"), "text/html") || !bytes.Contains(body, []byte("<html")) {
					http.Error(w, "Invalid desktop index", http.StatusInternalServerError)
					return
				}
				body = bytes.Replace(body, []byte("<html"), []byte(`<html data-omni-desktop="wails3"`), 1)
			}
			for key, values := range response.header {
				w.Header()[key] = values
			}
			w.Header().Del("Content-Length")
			w.Header().Del("ETag")
			w.Header().Set("Cache-Control", "no-store")
			if token != "" && response.status == http.StatusOK {
				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			if response.status == 0 {
				response.status = http.StatusInternalServerError
			}
			w.WriteHeader(response.status)
			_, _ = w.Write(body)
		})
	}, nil
}
