package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServerHost            = "127.0.0.1"
	defaultServerPort            = "4000"
	defaultAuthKDFConcurrency    = 2
	maximumAuthKDFConcurrency    = 16
	defaultServerShutdownTimeout = 30 * time.Second
	maximumServerShutdownTimeout = 5 * time.Minute
)

// ServerConfig is the complete process-level configuration needed before the
// production multi-user server opens a control database or public listener.
// It deliberately has no legacy single-user database or bcrypt fields.
type ServerConfig struct {
	ControlDBPath              string
	ControlDBEncryptionKeyFile string
	VaultRoot                  string
	InitialAdminSetupTokenFile string
	AuthKDFConcurrency         int
	DataAtRestMode             string
	DataAtRestAttestationFile  string
	ListenHost                 string
	Port                       string
	TLSCertFile                string
	TLSKeyFile                 string
	WebTransport               WebTransportConfig
	ShutdownTimeout            time.Duration
	Passkeys                   PasskeyConfig
}

var legacySingleUserServerEnv = []string{
	"AUTH_PASSWORD_HASH",
	"AUTH_REQUIRE_TOTP",
	"AUTH_TOTP_SECRET_FILE",
	"DB_PATH",
	"DB_ENCRYPTION_KEY_FILE",
}

// Static AI credentials are not bound to a control-plane user, vault, or DEK.
// Reject every legacy AI switch instead of silently starting an unscoped API.
var unsupportedMultiUserAIEnv = []string{
	"AI_API_TOKEN",
	"AI_CREDENTIALS_FILE",
	"AI_CONSOLE_TOKEN_FILE",
	"AI_AUDIT_HMAC_KEYRING_FILE",
	"AI_HOST_IP",
	"AI_PORT",
	"AI_ALLOW_REMOTE",
	"AI_TLS_CERT_FILE",
	"AI_TLS_KEY_FILE",
	"AI_TLS_CLIENT_CA_FILE",
	"AI_TLS_CA_FILE",
	"AI_TLS_CLIENT_CERT_FILE",
	"AI_TLS_CLIENT_KEY_FILE",
}

// ServerConfigFromEnv parses production multi-user configuration strictly.
// Invalid security-sensitive settings are startup errors, never fallbacks.
func ServerConfigFromEnv() (ServerConfig, error) {
	for _, name := range legacySingleUserServerEnv {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ServerConfig{}, fmt.Errorf("%s is a legacy single-user setting and is not supported by the production multi-user server", name)
		}
	}
	for _, name := range unsupportedMultiUserAIEnv {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ServerConfig{}, fmt.Errorf("%s is not supported until AI credentials are bound to a user vault", name)
		}
	}

	controlDBPath, err := requiredAbsolutePath("CONTROL_DB_PATH")
	if err != nil {
		return ServerConfig{}, err
	}
	controlKeyFile, err := requiredAbsolutePath("CONTROL_DB_ENCRYPTION_KEY_FILE")
	if err != nil {
		return ServerConfig{}, err
	}
	vaultRoot, err := requiredAbsolutePath("VAULT_ROOT")
	if err != nil {
		return ServerConfig{}, err
	}
	setupTokenFile, err := optionalAbsolutePath("INITIAL_ADMIN_SETUP_TOKEN_FILE")
	if err != nil {
		return ServerConfig{}, err
	}
	attestationFile, err := requiredAbsolutePath("DATA_AT_REST_ATTESTATION_FILE")
	if err != nil {
		return ServerConfig{}, err
	}
	if controlDBPath == controlKeyFile || controlKeyFile == attestationFile ||
		(setupTokenFile != "" && (controlDBPath == setupTokenFile || controlKeyFile == setupTokenFile || setupTokenFile == attestationFile)) {
		return ServerConfig{}, errors.New("control database, encryption key, setup token, and attestation must use distinct paths")
	}
	if pathContains(vaultRoot, controlDBPath) {
		return ServerConfig{}, errors.New("CONTROL_DB_PATH must not be inside VAULT_ROOT")
	}

	kdfConcurrency, err := boundedPositiveIntEnv(
		"AUTH_KDF_CONCURRENCY",
		defaultAuthKDFConcurrency,
		1,
		maximumAuthKDFConcurrency,
	)
	if err != nil {
		return ServerConfig{}, err
	}
	shutdownSeconds, err := boundedPositiveIntEnv(
		"SERVER_SHUTDOWN_TIMEOUT_SECONDS",
		int(defaultServerShutdownTimeout/time.Second),
		1,
		int(maximumServerShutdownTimeout/time.Second),
	)
	if err != nil {
		return ServerConfig{}, err
	}

	host := strings.TrimSpace(os.Getenv("HOST_IP"))
	if host == "" {
		host = defaultServerHost
	}
	if strings.ContainsAny(host, "\x00\r\n") {
		return ServerConfig{}, errors.New("HOST_IP contains invalid characters")
	}
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = defaultServerPort
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return ServerConfig{}, errors.New("PORT must be an integer between 1 and 65535")
	}
	// JoinHostPort also rejects malformed literal IPv6 forms at listener setup;
	// perform it here so embedded delimiters cannot alter the configured port.
	if _, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, port)); err != nil {
		return ServerConfig{}, fmt.Errorf("invalid HOST_IP or PORT: %w", err)
	}

	forceHTTPS, err := strictBoolEnv("FORCE_HTTPS", false)
	if err != nil {
		return ServerConfig{}, err
	}
	allowInsecureHTTP, err := strictBoolEnv("ALLOW_INSECURE_HTTP", false)
	if err != nil {
		return ServerConfig{}, err
	}
	tlsCertFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	tlsKeyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	externalHost := strings.TrimSpace(os.Getenv("WEB_EXTERNAL_HOST"))
	trustedProxies := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	transport := WebTransportConfig{
		ListenHost:        host,
		ExternalHost:      externalHost,
		TLSCertFile:       tlsCertFile,
		TLSKeyFile:        tlsKeyFile,
		ForceHTTPS:        forceHTTPS,
		TrustedProxies:    trustedProxies,
		AllowInsecureHTTP: allowInsecureHTTP,
	}
	if err := ValidateWebTransport(transport); err != nil {
		return ServerConfig{}, err
	}
	exposureHost := externalHost
	if exposureHost == "" {
		exposureHost = host
	}
	if tlsCertFile == "" && !IsLoopbackHost(exposureHost) && allowInsecureHTTP {
		return ServerConfig{}, errors.New("ALLOW_INSECURE_HTTP cannot permit non-loopback plaintext in the production multi-user server")
	}

	mode := strings.TrimSpace(os.Getenv("DATA_AT_REST_MODE"))
	if mode == "" {
		return ServerConfig{}, errors.New("DATA_AT_REST_MODE is required")
	}
	passkeys, err := passkeyConfigFromEnv(transport, host, strconv.Itoa(portNumber))
	if err != nil {
		return ServerConfig{}, err
	}

	return ServerConfig{
		ControlDBPath:              controlDBPath,
		ControlDBEncryptionKeyFile: controlKeyFile,
		VaultRoot:                  vaultRoot,
		InitialAdminSetupTokenFile: setupTokenFile,
		AuthKDFConcurrency:         kdfConcurrency,
		DataAtRestMode:             mode,
		DataAtRestAttestationFile:  attestationFile,
		ListenHost:                 host,
		Port:                       strconv.Itoa(portNumber),
		TLSCertFile:                tlsCertFile,
		TLSKeyFile:                 tlsKeyFile,
		WebTransport:               transport,
		ShutdownTimeout:            time.Duration(shutdownSeconds) * time.Second,
		Passkeys:                   passkeys,
	}, nil
}

func requiredAbsolutePath(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	value = filepath.Clean(value)
	if value == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be the filesystem root", name)
	}
	return value, nil
}

func optionalAbsolutePath(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path when configured", name)
	}
	value = filepath.Clean(value)
	if value == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be the filesystem root", name)
	}
	return value, nil
}

func strictBoolEnv(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func boundedPositiveIntEnv(name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
