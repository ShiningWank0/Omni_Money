//go:build omni_wails_browser

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The gateway protects every route, including the event/stream websocket routes
// mounted outside Wails' asset middleware. It is compiled only for CI diagnostics.
func configureWailsBrowserHost(options *application.Options) error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("WAILS_SERVER_HOST") != "127.0.0.1" {
		return errors.New("Wails browser diagnostics require an isolated CI loopback host")
	}
	port := func(name string) (int, error) {
		value, err := strconv.Atoi(os.Getenv(name))
		if err != nil || value < 1 || value > 65535 {
			return 0, errors.New("invalid CI Wails browser port")
		}
		return value, nil
	}
	backendPort, err := port("WAILS_SERVER_PORT")
	if err != nil {
		return err
	}
	publicPort, err := port("OMNI_WAILS_BROWSER_PORT")
	if err != nil {
		return err
	}
	if publicPort == backendPort {
		return errors.New("diagnostic listeners must be distinct")
	}
	origin := "http://127.0.0.1:" + strconv.Itoa(publicPort)
	backend, err := url.Parse("http://127.0.0.1:" + strconv.Itoa(backendPort))
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	proxyToken := base64.RawURLEncoding.EncodeToString(secret)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = backend.Host
		r.Header.Set("X-Omni-Diagnostic-Proxy", proxyToken)
	}
	// Direct access to the backend cannot invoke HTTP bindings without the
	// per-process gateway credential. Event/stream sockets separately enforce
	// the public Origin through Wails' server origin policy.
	options.Assets.Middleware = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Omni-Diagnostic-Proxy")), []byte(proxyToken)) != 1 {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	var gatewayHandler http.Handler = proxy
	if rawURL := os.Getenv("OMNI_WAILS_VITE_URL"); rawURL != "" {
		vite, err := url.Parse(rawURL)
		if err != nil || vite.Scheme != "http" || vite.Hostname() != "127.0.0.1" || vite.Port() == "" || vite.Path != "" {
			return errors.New("invalid CI Vite loopback URL")
		}
		viteProxy := httputil.NewSingleHostReverseProxy(vite)
		options.Assets.Handler = viteProxy
		gatewayHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/__vite_hmr" {
				viteProxy.ServeHTTP(w, r)
				return
			}
			proxy.ServeHTTP(w, r)
		})
	} else {
		directory := os.Getenv("OMNI_WAILS_ASSET_DIR")
		base := os.Getenv("RUNNER_TEMP")
		rel, err := filepath.Rel(base, directory)
		if base == "" || !filepath.IsAbs(directory) || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("diagnostic assets must be inside RUNNER_TEMP")
		}
		options.Assets.Handler = http.FileServer(os.DirFS(directory))
	}
	middleware, err := newDesktopAssetMiddleware(origin)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(publicPort))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: middleware(gatewayHandler), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	go func() { _ = server.Serve(listener) }()
	options.Server.Host = "127.0.0.1"
	options.Server.Port = backendPort
	options.Server.ShutdownTimeout = 2 * time.Second
	options.Server.WebSocketOriginPatterns = []string{origin}
	options.OnShutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
	return nil
}
