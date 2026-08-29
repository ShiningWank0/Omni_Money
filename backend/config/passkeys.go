package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
)

type PasskeyConfig struct {
	RPID    string
	Origins []string
}

func passkeyConfigFromEnv(transport WebTransportConfig, listenHost, port string) (PasskeyConfig, error) {
	rpID := strings.TrimSpace(os.Getenv("PASSKEY_RP_ID"))
	allowedHosts := splitNonEmpty(os.Getenv("ALLOWED_HOSTS"))
	if rpID == "" {
		candidate := strings.TrimSpace(os.Getenv("HTTPS_REDIRECT_HOST"))
		if candidate == "" && len(allowedHosts) > 0 {
			candidate = allowedHosts[0]
		}
		if candidate == "" {
			candidate = strings.TrimSpace(transport.ExternalHost)
		}
		if candidate == "" {
			candidate = listenHost
		}
		rpID = hostWithoutPort(candidate)
	}
	rpID = strings.ToLower(strings.TrimSuffix(rpID, "."))
	if rpID == "" || strings.ContainsAny(rpID, "/@\x00\r\n\t ") || (strings.Contains(rpID, ":") && net.ParseIP(rpID) == nil) {
		return PasskeyConfig{}, errors.New("PASSKEY_RP_ID must be a hostname without a scheme, path, or port")
	}

	origins := splitNonEmpty(os.Getenv("PASSKEY_ORIGINS"))
	if len(origins) == 0 {
		hosts := allowedHosts
		if len(hosts) == 0 {
			host := strings.TrimSpace(transport.ExternalHost)
			if host == "" {
				host = listenHost
			}
			if _, _, err := net.SplitHostPort(host); err != nil && port != "" {
				host = net.JoinHostPort(strings.Trim(host, "[]"), port)
			}
			hosts = []string{host}
		}
		scheme := "http"
		if transport.ForceHTTPS || strings.TrimSpace(transport.TLSCertFile) != "" {
			scheme = "https"
		}
		for _, host := range hosts {
			hostname := hostWithoutPort(host)
			if hostname == rpID || strings.HasSuffix(strings.ToLower(hostname), "."+strings.ToLower(rpID)) {
				origins = append(origins, scheme+"://"+host)
			}
		}
	}
	if len(origins) == 0 {
		return PasskeyConfig{}, errors.New("PASSKEY_ORIGINS has no origin matching PASSKEY_RP_ID")
	}
	seen := map[string]struct{}{}
	validated := make([]string, 0, len(origins))
	for _, raw := range origins {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return PasskeyConfig{}, fmt.Errorf("invalid PASSKEY_ORIGINS entry %q", raw)
		}
		hostname := parsed.Hostname()
		if strings.ToLower(hostname) != rpID && !strings.HasSuffix(strings.ToLower(hostname), "."+rpID) {
			return PasskeyConfig{}, fmt.Errorf("PASSKEY_ORIGINS entry %q is outside RP ID %q", raw, rpID)
		}
		if parsed.Scheme == "http" && !IsLoopbackHost(hostname) {
			return PasskeyConfig{}, fmt.Errorf("PASSKEY_ORIGINS entry %q must use HTTPS", raw)
		}
		origin := parsed.Scheme + "://" + parsed.Host
		if _, ok := seen[origin]; !ok {
			seen[origin] = struct{}{}
			validated = append(validated, origin)
		}
	}
	sort.Strings(validated)
	return PasskeyConfig{RPID: rpID, Origins: validated}, nil
}

func splitNonEmpty(raw string) []string {
	result := make([]string, 0)
	for _, token := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(token); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func hostWithoutPort(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}
