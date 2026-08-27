// Package aitransport configures the isolated AI listener's authenticated TLS transport.
package aitransport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"

	"omni_money/backend/secretfile"
)

const maxTLSFileBytes = 2 * 1024 * 1024

// IsLoopbackHost reports whether host is a literal loopback address. Hostnames
// are deliberately not trusted because name resolution can be reconfigured.
func IsLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BuildServerTLSConfig returns nil only for a loopback-only plaintext listener.
// A non-loopback listener requires TLS 1.3 and a client CA (mTLS).
func BuildServerTLSConfig(host, certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	clientCAFile = strings.TrimSpace(clientCAFile)
	remote := !IsLoopbackHost(host)

	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("AI_TLS_CERT_FILE と AI_TLS_KEY_FILE は両方指定してください")
	}
	if certFile == "" {
		if clientCAFile != "" {
			return nil, fmt.Errorf("AI_TLS_CLIENT_CA_FILE を使う場合はAI TLS証明書と鍵が必要です")
		}
		if remote {
			return nil, fmt.Errorf("非ループバックのAIリスナーにはTLS証明書、鍵、クライアントCAが必要です")
		}
		return nil, nil
	}
	if remote && clientCAFile == "" {
		return nil, fmt.Errorf("非ループバックのAIリスナーにはAI_TLS_CLIENT_CA_FILEによるmTLSが必要です")
	}

	certPEM, err := readCheckedFile(certFile, false)
	if err != nil {
		return nil, fmt.Errorf("AI TLS証明書: %w", err)
	}
	keyPEM, err := readCheckedFile(keyFile, true)
	if err != nil {
		return nil, fmt.Errorf("AI TLS秘密鍵: %w", err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("AI TLS証明書と鍵が無効です: %w", err)
	}

	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	if clientCAFile != "" {
		clientCAPEM, err := readCheckedFile(clientCAFile, false)
		if err != nil {
			return nil, fmt.Errorf("AI TLSクライアントCA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(clientCAPEM) {
			return nil, fmt.Errorf("AI TLSクライアントCAに有効な証明書がありません")
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

// BuildClientTLSConfig creates a verifying TLS client configuration. It never
// enables InsecureSkipVerify and loads an optional mTLS client identity.
func BuildClientTLSConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	caFile = strings.TrimSpace(caFile)
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	if caFile == "" {
		return nil, fmt.Errorf("AI_TLS_CA_FILE は必須です")
	}
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("AI_TLS_CLIENT_CERT_FILE と AI_TLS_CLIENT_KEY_FILE は両方指定してください")
	}

	caPEM, err := readCheckedFile(caFile, false)
	if err != nil {
		return nil, fmt.Errorf("AI TLSサーバーCA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("AI TLSサーバーCAに有効な証明書がありません")
	}
	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: strings.TrimSpace(serverName),
	}
	if certFile != "" {
		certPEM, err := readCheckedFile(certFile, false)
		if err != nil {
			return nil, fmt.Errorf("AI TLSクライアント証明書: %w", err)
		}
		keyPEM, err := readCheckedFile(keyFile, true)
		if err != nil {
			return nil, fmt.Errorf("AI TLSクライアント秘密鍵: %w", err)
		}
		certificate, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("AI TLSクライアント証明書と鍵が無効です: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func readCheckedFile(path string, secret bool) ([]byte, error) {
	if secret {
		return secretfile.ReadConfidential(path, maxTLSFileBytes)
	}
	return secretfile.ReadIntegrityProtected(path, maxTLSFileBytes)
}
