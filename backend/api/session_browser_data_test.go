package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/vault"
)

func assertSessionBrowserCleanup(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	// In particular, neither "cookies" nor "*" may erase the proxy's
	// authentication, even if it shares this origin or a sibling subdomain.
	if got := response.Header().Get("Clear-Site-Data"); got != `"cache", "storage"` {
		t.Fatalf("unsafe browser cleanup: %q", got)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected exactly the two application deletion cookies: %+v", cookies)
	}
	seen := map[string]bool{}
	for _, cookie := range cookies {
		if cookie.Name != middleware.SessionCookieName && cookie.Name != middleware.SecureSessionCookieName {
			t.Fatalf("deleted a non-application cookie: %s", cookie.Name)
		}
		seen[cookie.Name] = true
		if cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.Expires.Before(time.Now()) || cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("session deletion lost its security attributes: %+v", cookie)
		}
		if cookie.Name == middleware.SecureSessionCookieName && !cookie.Secure {
			t.Fatal("__Host- deletion cookie lost Secure")
		}
	}
	if len(seen) != 2 {
		t.Fatal("did not delete both application cookie names")
	}
}

func TestSessionBrowserCleanupPreservesProxyAndSiblingCookies(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			sessions := middleware.NewSessionManager(time.Hour)
			t.Cleanup(sessions.Close)
			origin, _ := url.Parse(scheme + "://money.example.test/")
			sibling, _ := url.Parse(scheme + "://photos.example.test/")
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(origin, []*http.Cookie{
				{Name: middleware.SessionCookieName, Value: "old-app-session", Path: "/"},
				{Name: middleware.SecureSessionCookieName, Value: "old-secure-app-session", Path: "/", Secure: true},
				{Name: "p_session_token_s.123", Value: "proxy-session", Path: "/", HttpOnly: true},
				{Name: "parent-sso", Value: "shared-session", Domain: "example.test", Path: "/", HttpOnly: true},
			})
			jar.SetCookies(sibling, []*http.Cookie{{Name: "photos-session", Value: "sibling-session", Path: "/"}})
			request := httptest.NewRequest(http.MethodPost, origin.String()+"api/auth/logout", nil)
			response := httptest.NewRecorder()
			clearSessionBrowserData(response, request, sessions)
			assertSessionBrowserCleanup(t, response)
			jar.SetCookies(origin, response.Result().Cookies())
			for _, site := range []*url.URL{origin, sibling} {
				values := map[string]string{}
				for _, cookie := range jar.Cookies(site) {
					values[cookie.Name] = cookie.Value
					if cookie.Name == middleware.SessionCookieName || cookie.Name == middleware.SecureSessionCookieName {
						t.Fatal("application session cookie survived deletion")
					}
				}
				if values["parent-sso"] != "shared-session" || (site == origin && values["p_session_token_s.123"] != "proxy-session") || (site == sibling && values["photos-session"] != "sibling-session") {
					t.Fatalf("unrelated authentication was modified for %s: %+v", site, values)
				}
			}
		})
	}
}

func TestServerLogoutBehindProxyStillRevokesApplicationAuthority(t *testing.T) {
	for _, path := range []string{"/api/auth/logout", "/api/auth/logout-all"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("ALLOWED_HOSTS", "money.example.test")
			t.Setenv("FORCE_HTTPS", "false")
			t.Setenv("TRUSTED_PROXIES", "")
			t.Setenv("HTTPS_REDIRECT_HOST", "")
			manager, err := vault.NewManager(filepath.Join(t.TempDir(), "vaults"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			owner := control.UserSummary{ID: "user_01HZX7CYK3XPSJ0HE8P2RQ7V4M", Email: "owner@example.test", Role: control.RoleUser, State: control.UserActive}
			root := acquireServerCSVVault(t, manager, owner.ID, "vault_01HZX7CYK3XPSJ0HE8P2RQ7V4M", 0x51)
			sessions := middleware.NewSessionManager(time.Hour)
			t.Cleanup(sessions.Close)
			session, err := sessions.CreateVaultSession(owner, root)
			if err != nil {
				root.Release()
				t.Fatal(err)
			}
			handler, err := NewServerRouter(ServerDependencies{
				Accounts: &fakeServerAccounts{}, Sessions: sessions,
				Control: &fakeServerControl{bootstrapped: true, user: owner},
			})
			if err != nil {
				t.Fatal(err)
			}
			forged := serverJSONRequest(t, http.MethodPost, path, "")
			forged.AddCookie(&http.Cookie{Name: middleware.SecureSessionCookieName, Value: session.ID})
			rejected := serveSecurityRequest(handler, forged)
			if rejected.Code != http.StatusForbidden || rejected.Header().Get("Clear-Site-Data") != "" {
				t.Fatalf("CSRF rejection changed: %d %s", rejected.Code, rejected.Body.String())
			}
			if _, ok := sessions.GetSession(session.ID); !ok {
				t.Fatal("forged logout revoked a valid session")
			}
			response := serveServerCSVRequest(t, handler, session, http.MethodPost, path, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("logout=%d %s", response.Code, response.Body.String())
			}
			assertSessionBrowserCleanup(t, response)
			if _, ok := sessions.GetSession(session.ID); ok {
				t.Fatal("old session retained authority")
			}
			if stale := serveServerCSVRequest(t, handler, session, http.MethodGet, "/api/transactions", nil); stale.Code != http.StatusUnauthorized {
				t.Fatalf("old cookie can still read the ledger: %d", stale.Code)
			}
			status := serveServerCSVRequest(t, handler, session, http.MethodGet, "/api/auth/status", nil)
			var data struct {
				Authenticated bool `json:"authenticated"`
			}
			if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &data) != nil || data.Authenticated {
				t.Fatalf("logout cannot be confirmed: %d %s", status.Code, status.Body.String())
			}
		})
	}
}
