package config

import (
	"fmt"
	"net"
	"strings"
)

// WebTransportConfig は公開Webリスナーの実効的な外部公開条件を表す。
// ExternalHost はDockerのポート公開先など、ListenHostと異なる場合に指定する。
type WebTransportConfig struct {
	ListenHost        string
	ExternalHost      string
	TLSCertFile       string
	TLSKeyFile        string
	ForceHTTPS        bool
	TrustedProxies    string
	AllowInsecureHTTP bool
}

// ValidateWebTransport は認証情報と財務データを平文で外部公開しないことを検証する。
func ValidateWebTransport(cfg WebTransportConfig) error {
	certFile := strings.TrimSpace(cfg.TLSCertFile)
	keyFile := strings.TrimSpace(cfg.TLSKeyFile)
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("TLS_CERT_FILE と TLS_KEY_FILE は両方指定してください")
	}
	if certFile != "" {
		return nil
	}

	exposureHost := strings.TrimSpace(cfg.ExternalHost)
	if exposureHost == "" {
		exposureHost = strings.TrimSpace(cfg.ListenHost)
	}
	if IsLoopbackHost(exposureHost) {
		return nil
	}

	// TLS終端プロキシ配下では、信頼する送信元を限定したうえでHTTPSを強制する。
	if cfg.ForceHTTPS && hasValidTrustedProxy(cfg.TrustedProxies) {
		return nil
	}
	if cfg.AllowInsecureHTTP {
		return nil
	}

	return fmt.Errorf("非ループバックアドレス %q への平文HTTP公開は拒否されました。TLSを設定するか、TRUSTED_PROXIESとFORCE_HTTPS=trueでHTTPSプロキシを構成してください。閉域網でリスクを受容する場合のみ ALLOW_INSECURE_HTTP=true を明示してください", exposureHost)
}

func hasValidTrustedProxy(raw string) bool {
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.Contains(token, "/") {
			if _, _, err := net.ParseCIDR(token); err == nil {
				return true
			}
			continue
		}
		if net.ParseIP(token) != nil {
			return true
		}
	}
	return false
}

// IsLoopbackHost はホスト名またはIPがループバックを表すかを返す。
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
