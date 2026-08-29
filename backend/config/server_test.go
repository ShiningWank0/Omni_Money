package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setValidServerEnvironment(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	values := map[string]string{
		"CONTROL_DB_PATH":                 filepath.Join(root, "data", "control", "omni_control.db"),
		"CONTROL_DB_ENCRYPTION_KEY_FILE":  filepath.Join(root, "secrets", "control.key"),
		"VAULT_ROOT":                      filepath.Join(root, "data", "vaults"),
		"INITIAL_ADMIN_SETUP_TOKEN_FILE":  filepath.Join(root, "secrets", "setup.token"),
		"DATA_AT_REST_MODE":               "external-encrypted-volume",
		"DATA_AT_REST_ATTESTATION_FILE":   filepath.Join(root, "secrets", "at-rest.json"),
		"HOST_IP":                         "127.0.0.1",
		"PORT":                            "4000",
		"FORCE_HTTPS":                     "false",
		"ALLOW_INSECURE_HTTP":             "false",
		"WEB_EXTERNAL_HOST":               "",
		"TRUSTED_PROXIES":                 "",
		"TLS_CERT_FILE":                   "",
		"TLS_KEY_FILE":                    "",
		"AUTH_KDF_CONCURRENCY":            "",
		"SERVER_SHUTDOWN_TIMEOUT_SECONDS": "",
		"PASSKEY_RP_ID":                   "",
		"PASSKEY_ORIGINS":                 "",
	}
	for _, name := range legacySingleUserServerEnv {
		values[name] = ""
	}
	for _, name := range unsupportedMultiUserAIEnv {
		values[name] = ""
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestServerConfigFromEnvParsesSecureDefaults(t *testing.T) {
	setValidServerEnvironment(t)

	result, err := ServerConfigFromEnv()
	if err != nil {
		t.Fatalf("ServerConfigFromEnv: %v", err)
	}
	if result.AuthKDFConcurrency != defaultAuthKDFConcurrency {
		t.Fatalf("AuthKDFConcurrency = %d", result.AuthKDFConcurrency)
	}
	if result.ShutdownTimeout != defaultServerShutdownTimeout {
		t.Fatalf("ShutdownTimeout = %s", result.ShutdownTimeout)
	}
	if result.ListenHost != "127.0.0.1" || result.Port != "4000" {
		t.Fatalf("listener = %s:%s", result.ListenHost, result.Port)
	}
	if result.WebTransport.AllowInsecureHTTP {
		t.Fatal("insecure HTTP unexpectedly enabled")
	}
}

func TestServerConfigFromEnvParsesPasskeyBoundary(t *testing.T) {
	setValidServerEnvironment(t)
	t.Setenv("HOST_IP", "0.0.0.0")
	t.Setenv("FORCE_HTTPS", "true")
	t.Setenv("TRUSTED_PROXIES", "172.30.240.3/32")
	t.Setenv("PASSKEY_RP_ID", "Money.Example.COM")
	t.Setenv("PASSKEY_ORIGINS", "https://money.example.com,https://family.money.example.com")
	result, err := ServerConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if result.Passkeys.RPID != "money.example.com" || len(result.Passkeys.Origins) != 2 {
		t.Fatalf("unexpected passkey config: %+v", result.Passkeys)
	}

	t.Setenv("PASSKEY_ORIGINS", "http://money.example.com")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure remote passkey origin accepted: %v", err)
	}

	t.Setenv("PASSKEY_ORIGINS", "https://unrelated.example.net")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "outside RP ID") {
		t.Fatalf("cross-site passkey origin accepted: %v", err)
	}
}

func TestServerConfigFromEnvRequiresAbsoluteSecurityPaths(t *testing.T) {
	pathNames := []string{
		"CONTROL_DB_PATH",
		"CONTROL_DB_ENCRYPTION_KEY_FILE",
		"VAULT_ROOT",
		"DATA_AT_REST_ATTESTATION_FILE",
	}
	for _, name := range pathNames {
		t.Run(name+" missing", func(t *testing.T) {
			setValidServerEnvironment(t)
			t.Setenv(name, "")
			if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("missing %s was accepted: %v", name, err)
			}
		})
		t.Run(name+" relative", func(t *testing.T) {
			setValidServerEnvironment(t)
			t.Setenv(name, "relative/path")
			if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "absolute") {
				t.Fatalf("relative %s was accepted: %v", name, err)
			}
		})
	}
}

func TestServerConfigFromEnvAllowsSetupTokenToBeUnmountedAfterBootstrap(t *testing.T) {
	setValidServerEnvironment(t)
	t.Setenv("INITIAL_ADMIN_SETUP_TOKEN_FILE", "")
	result, err := ServerConfigFromEnv()
	if err != nil {
		t.Fatalf("optional setup token rejected: %v", err)
	}
	if result.InitialAdminSetupTokenFile != "" {
		t.Fatalf("setup token path = %q", result.InitialAdminSetupTokenFile)
	}

	t.Setenv("INITIAL_ADMIN_SETUP_TOKEN_FILE", "relative/token")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative optional setup token was accepted: %v", err)
	}
}

func TestServerConfigFromEnvRejectsLegacyAndAISettings(t *testing.T) {
	for _, name := range append(append([]string{}, legacySingleUserServerEnv...), unsupportedMultiUserAIEnv...) {
		t.Run(name, func(t *testing.T) {
			setValidServerEnvironment(t)
			t.Setenv(name, "configured")
			if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s was accepted: %v", name, err)
			}
		})
	}
}

func TestServerConfigFromEnvBoundsKDFAndShutdown(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"AUTH_KDF_CONCURRENCY", "0"},
		{"AUTH_KDF_CONCURRENCY", "17"},
		{"AUTH_KDF_CONCURRENCY", "many"},
		{"SERVER_SHUTDOWN_TIMEOUT_SECONDS", "0"},
		{"SERVER_SHUTDOWN_TIMEOUT_SECONDS", "301"},
	}
	for _, test := range tests {
		t.Run(test.name+"="+test.value, func(t *testing.T) {
			setValidServerEnvironment(t)
			t.Setenv(test.name, test.value)
			if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("invalid value was accepted: %v", err)
			}
		})
	}

	setValidServerEnvironment(t)
	t.Setenv("AUTH_KDF_CONCURRENCY", "16")
	t.Setenv("SERVER_SHUTDOWN_TIMEOUT_SECONDS", "300")
	result, err := ServerConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthKDFConcurrency != 16 || result.ShutdownTimeout != 5*time.Minute {
		t.Fatalf("bounded settings not preserved: %+v", result)
	}
}

func TestServerConfigFromEnvRejectsNonLoopbackInsecureHTTP(t *testing.T) {
	setValidServerEnvironment(t)
	t.Setenv("HOST_IP", "0.0.0.0")
	t.Setenv("ALLOW_INSECURE_HTTP", "true")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "ALLOW_INSECURE_HTTP") {
		t.Fatalf("non-loopback plaintext exception was accepted: %v", err)
	}

	setValidServerEnvironment(t)
	t.Setenv("HOST_IP", "0.0.0.0")
	t.Setenv("FORCE_HTTPS", "true")
	t.Setenv("TRUSTED_PROXIES", "172.30.240.3/32")
	if _, err := ServerConfigFromEnv(); err != nil {
		t.Fatalf("strict HTTPS proxy configuration rejected: %v", err)
	}

	setValidServerEnvironment(t)
	t.Setenv("HOST_IP", "0.0.0.0")
	t.Setenv("WEB_EXTERNAL_HOST", "127.0.0.1")
	if _, err := ServerConfigFromEnv(); err != nil {
		t.Fatalf("loopback-only effective exposure rejected: %v", err)
	}
}

func TestServerConfigFromEnvRejectsAmbiguousPathsAndValues(t *testing.T) {
	setValidServerEnvironment(t)
	controlPath := filepath.Join(t.TempDir(), "vaults", "control.db")
	t.Setenv("VAULT_ROOT", filepath.Dir(controlPath))
	t.Setenv("CONTROL_DB_PATH", controlPath)
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "VAULT_ROOT") {
		t.Fatalf("control database inside vault root was accepted: %v", err)
	}

	setValidServerEnvironment(t)
	t.Setenv("FORCE_HTTPS", "yes")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "FORCE_HTTPS") {
		t.Fatalf("ambiguous boolean was accepted: %v", err)
	}

	setValidServerEnvironment(t)
	t.Setenv("PORT", "0")
	if _, err := ServerConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("invalid port was accepted: %v", err)
	}
}
