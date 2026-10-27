package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func frontendFixture(t *testing.T, root string) {
	t.Helper()
	for name, body := range map[string]string{
		".env":                       "not-public",
		"index.html":                 "<!doctype html><title>Omni Money</title>",
		"favicon.svg":                "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>",
		"assets/favicon-test.svg":    "<svg>fixed icon bytes</svg>",
		"assets/index-test.js":       "console.log('app')",
		"assets/index-test.css":      "body { color: black }",
		"assets/.env":                "not-public",
		"assets/debug.txt":           "not-public",
		"assets/debug.js.map":        "not-public",
		"assets/upload.php":          "not-public",
		"assets/upload.html":         "not-public",
		"assets/.private/hidden.svg": "not-public",
	} {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "key.svg"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"assets/linked-key.svg":   filepath.Join(outside, "key.svg"),
		"assets/linked-dir":       outside,
		"assets/internal-key.svg": "../.env",
		"assets/internal-dir":     ".private",
	} {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFrontendFilesAreReadOnlyAndBounded(t *testing.T) {
	root := t.TempDir()
	frontendFixture(t, root)
	handler := frontendFileHandler(root)
	for _, target := range []string{"/", "/index.html", "/login", "/login/", "/login?next=/api/admin/users", "/favicon.svg", "/assets/favicon-test.svg?file=../../key.svg", "/assets/index-test.js", "/assets/index-test.css"} {
		t.Run(target, func(t *testing.T) {
			get := httptest.NewRecorder()
			handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, target, nil))
			if get.Code != http.StatusOK || get.Body.Len() == 0 {
				t.Fatalf("GET: status=%d body=%q", get.Code, get.Body.String())
			}
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, target, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != get.Header().Get("Content-Type") {
				t.Fatalf("HEAD: status=%d body=%q headers=%v", head.Code, head.Body.String(), head.Header())
			}
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(method, target, strings.NewReader("untrusted upload")))
				if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("%s: status=%d allow=%q", method, recorder.Code, recorder.Header().Get("Allow"))
				}
			}
		})
	}

	for _, target := range []string{"/assets/", "/assets", "/favicon.ico", "/favicon.ico/shell.php", "/.env", "/assets/.env", "/assets/debug.txt", "/assets/debug.js.map", "/assets/upload.php", "/assets/upload.html", "/assets/.private/hidden.svg", "/assets/linked-key.svg", "/assets/linked-dir/key.svg", "/assets/internal-key.svg", "/assets/internal-dir/hidden.svg", "/assets/../key.svg", "/assets/%2e%2e/key.svg", "/assets/%2fetc/passwd", "/assets/%5c..%5ckey.svg", "/login/unregistered", "/api/transactions"} {
		t.Run("reject "+target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
			if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "not-public") || strings.Contains(recorder.Body.String(), root) {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestFrontendRetainsImageTypeAndHTTPReadSemantics(t *testing.T) {
	root := t.TempDir()
	frontendFixture(t, root)
	handler := frontendFileHandler(root)
	rangeRequest := httptest.NewRequest(http.MethodGet, "/assets/favicon-test.svg", nil)
	rangeRequest.Header.Set("Range", "bytes=0-3")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, rangeRequest)
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "<svg" || recorder.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("range: status=%d body=%q type=%q", recorder.Code, recorder.Body.String(), recorder.Header().Get("Content-Type"))
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/favicon-test.svg", nil)
	request.Header.Set("If-Modified-Since", recorder.Header().Get("Last-Modified"))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotModified || recorder.Body.Len() != 0 {
		t.Fatalf("conditional: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerFrontendCannotReachSecretsOrProtectedAPIs(t *testing.T) {
	project := t.TempDir()
	frontendFixture(t, filepath.Join(project, "frontend", "dist"))
	t.Chdir(project)
	handler := newTestServerRouter(t, &fakeServerAccounts{}, &fakeServerControl{bootstrapped: true})
	for _, target := range []string{"/", "/login", "/assets/favicon-test.svg"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://money.example.test"+target, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: status=%d headers=%v", target, recorder.Code, recorder.Header())
		}
	}
	for _, target := range []string{"/assets/", "/assets/linked-key.svg", "/assets/linked-dir/key.svg", "/assets/internal-key.svg", "/assets/internal-dir/hidden.svg", "/assets/.env", "/favicon.ico"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://money.example.test"+target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d body=%q", target, recorder.Code, recorder.Body.String())
		}
	}
	for _, target := range []string{"/api/transactions", "/api/admin/users"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://money.example.test"+target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status=%d body=%q", target, recorder.Code, recorder.Body.String())
		}
	}
	for _, target := range []string{"/assets/../api/transactions", "/assets/%2e%2e/api/transactions", "/assets%2f..%2fapi%2ftransactions", "/favicon.ico/../../api/admin/users"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://money.example.test"+target, nil))
		if recorder.Code == http.StatusMovedPermanently || recorder.Code == http.StatusTemporaryRedirect || recorder.Code == http.StatusPermanentRedirect {
			location := recorder.Header().Get("Location")
			if !strings.HasPrefix(location, "/api/") {
				t.Fatalf("unexpected redirect: %q", location)
			}
			recorder = httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://money.example.test"+location, nil))
		}
		if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d body=%q", target, recorder.Code, recorder.Body.String())
		}
	}
}
