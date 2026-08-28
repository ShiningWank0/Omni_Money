package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/middleware"
	"omni_money/backend/serverauth"
)

type fakeServerAccounts struct {
	bootstrapInput serverauth.BootstrapInput
	bootstrapErr   error
	loginErr       error
}

func (fake *fakeServerAccounts) Bootstrap(_ context.Context, input serverauth.BootstrapInput, _ time.Time) (control.UserSummary, error) {
	fake.bootstrapInput = input
	if fake.bootstrapErr != nil {
		return control.UserSummary{}, fake.bootstrapErr
	}
	return testServerUser(control.RoleAdmin), nil
}

func (*fakeServerAccounts) AcceptInvitation(context.Context, serverauth.AcceptInvitationInput, time.Time) (control.UserSummary, error) {
	return testServerUser(control.RoleUser), nil
}

func (fake *fakeServerAccounts) Login(context.Context, string, []byte, time.Time) (*middleware.Session, error) {
	if fake.loginErr != nil {
		return nil, fake.loginErr
	}
	return nil, serverauth.ErrServiceUnavailable
}

func (*fakeServerAccounts) Reauthenticate(context.Context, string, []byte, time.Time) error {
	return nil
}

func (*fakeServerAccounts) CreateInvitation(context.Context, string, string, control.Role, time.Time, time.Time) (control.Invitation, string, error) {
	return control.Invitation{}, "", nil
}

func (*fakeServerAccounts) CreatePasswordReset(context.Context, string, string, time.Time, time.Time) (control.PasswordResetTicket, string, error) {
	return control.PasswordResetTicket{}, "", nil
}

func (*fakeServerAccounts) CompletePasswordReset(context.Context, serverauth.CompletePasswordResetInput, time.Time) (control.PasswordResetTicket, error) {
	return control.PasswordResetTicket{}, nil
}

func (*fakeServerAccounts) DisableUser(context.Context, string, string, time.Time) error { return nil }

type fakeServerControl struct {
	bootstrapped bool
	user         control.UserSummary
}

func (fake *fakeServerControl) IsBootstrapped(context.Context) (bool, error) {
	return fake.bootstrapped, nil
}

func (fake *fakeServerControl) GetUser(context.Context, string) (control.UserSummary, error) {
	if fake.user.ID == "" {
		return control.UserSummary{}, control.ErrNotFound
	}
	return fake.user, nil
}

func (fake *fakeServerControl) ListUsers(context.Context) ([]control.UserSummary, error) {
	if fake.user.ID == "" {
		return []control.UserSummary{}, nil
	}
	return []control.UserSummary{fake.user}, nil
}

func testServerUser(role control.Role) control.UserSummary {
	return control.UserSummary{
		ID: "11111111-1111-4111-8111-111111111111", Email: "admin@example.com",
		DisplayName: "Admin", Role: role, State: control.UserActive,
		CreatedAt: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
	}
}

func newTestServerRouter(t *testing.T, accounts *fakeServerAccounts, controlStore *fakeServerControl) http.Handler {
	t.Helper()
	t.Setenv("ALLOWED_HOSTS", "money.example.test")
	t.Setenv("FORCE_HTTPS", "false")
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("HTTPS_REDIRECT_HOST", "")
	sessions := middleware.NewSessionManagerWithConfig(middleware.DefaultSessionConfig())
	t.Cleanup(sessions.Close)
	handler, err := NewServerRouter(ServerDependencies{
		Accounts: accounts, Sessions: sessions, Control: controlStore,
		Now: func() time.Time { return time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serverJSONRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "https://money.example.test"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://money.example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func TestServerBootstrapUsesByteSecretsAndClearsThem(t *testing.T) {
	accounts := &fakeServerAccounts{}
	handler := newTestServerRouter(t, accounts, &fakeServerControl{})
	body, err := json.Marshal(map[string]interface{}{
		"setup_token_b64":     []byte("abcdefghijklmnopqrstuvwxyzABCDEF"),
		"email":               "admin@example.com",
		"display_name":        "Admin",
		"password_b64":        []byte("correct horse battery staple"),
		"recovery_secret_b64": []byte("12345678901234567890123456789012"),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodPost, "/api/auth/setup", string(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	for name, secret := range map[string][]byte{
		"setup token": accounts.bootstrapInput.SetupToken,
		"password":    accounts.bootstrapInput.Password,
		"recovery":    accounts.bootstrapInput.RecoverySecret,
	} {
		for _, value := range secret {
			if value != 0 {
				t.Fatalf("%s was not cleared after service call", name)
			}
		}
	}
}

func TestServerAuthJSONRejectsDuplicateAndUnknownFields(t *testing.T) {
	handler := newTestServerRouter(t, &fakeServerAccounts{}, &fakeServerControl{})
	for name, body := range map[string]string{
		"duplicate": `{"email":"a@example.com","email":"b@example.com","password_b64":"cGFzc3dvcmQ="}`,
		"unknown":   `{"email":"a@example.com","password_b64":"cGFzc3dvcmQ=","actor_id":"attacker"}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodPost, "/api/auth/login", body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestServerLoginDoesNotExposeInternalErrors(t *testing.T) {
	accounts := &fakeServerAccounts{loginErr: errors.New("sensitive SQL path /app/data/control.db")}
	handler := newTestServerRouter(t, accounts, &fakeServerControl{bootstrapped: true})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodPost, "/api/auth/login", `{"email":"a@example.com","password_b64":"Y29ycmVjdCBob3JzZSBiYXR0ZXJ5IHN0YXBsZQ=="}`))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(recorder.Body.String(), "sensitive") || strings.Contains(recorder.Body.String(), "control.db") {
		t.Fatalf("internal error leaked: %s", recorder.Body.String())
	}
}

func TestServerPublicRouteExceptionsDoNotUsePrefixMatching(t *testing.T) {
	handler := newTestServerRouter(t, &fakeServerAccounts{}, &fakeServerControl{})
	for _, path := range []string{
		"/api/auth/setup/",
		"/api/auth/invitations/accept/",
		"/api/auth/password-reset/complete/",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, serverJSONRequest(t, http.MethodPost, path, `{}`))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusUnauthorized)
		}
	}
}

func TestNewServerRouterRejectsMissingDependencies(t *testing.T) {
	if _, err := NewServerRouter(ServerDependencies{}); err == nil {
		t.Fatal("missing server dependencies were accepted")
	}
}
