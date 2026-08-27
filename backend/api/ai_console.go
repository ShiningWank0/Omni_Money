package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"omni_money/backend/aitransport"
	"omni_money/backend/middleware"
	"omni_money/backend/secretfile"
)

const maxAIConsoleResponseSize = 10 * 1024 * 1024

var aiConsoleHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// handleAIConsoleProxy はセッション認証済みの管理UIから、固定された
// loopback上のAI専用リスナーへリクエストを中継する。URLとBearer tokenは
// ブラウザへ渡さず、任意URLへの転送も許可しない。
func handleAIConsoleProxy(aiPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		auditWriter := &aiConsoleAuditWriter{ResponseWriter: w, status: http.StatusOK}
		w = auditWriter
		defer func() {
			record := struct {
				Timestamp     string `json:"timestamp"`
				Operation     string `json:"operation"`
				ClientIP      string `json:"client_ip,omitempty"`
				SessionSHA256 string `json:"session_sha256,omitempty"`
				Status        int    `json:"status"`
				DurationMS    int64  `json:"duration_ms"`
			}{
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
				Operation:     aiPath,
				ClientIP:      middleware.ClientIPFromRequest(r),
				SessionSHA256: sessionAuditReference(r),
				Status:        auditWriter.status,
				DurationMS:    time.Since(started).Milliseconds(),
			}
			if encoded, err := json.Marshal(record); err == nil {
				log.Printf("AI_CONSOLE_AUDIT %s", encoded)
			}
		}()
		w.Header().Set("Cache-Control", "no-store")
		if !isAllowedAIConsolePath(aiPath) {
			jsonError(w, "AI専用APIの中継先が無効です", http.StatusInternalServerError)
			return
		}
		token, err := readAIConsoleToken()
		if err != nil {
			jsonError(w, "AI専用APIが有効化されていません", http.StatusServiceUnavailable)
			return
		}

		port := strings.TrimSpace(os.Getenv("AI_PORT"))
		if port == "" {
			port = "4001"
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			jsonError(w, "AI専用APIのポート設定が無効です", http.StatusInternalServerError)
			return
		}

		scheme := "http"
		client := aiConsoleHTTPClient
		if strings.TrimSpace(os.Getenv("AI_TLS_CERT_FILE")) != "" || strings.TrimSpace(os.Getenv("AI_TLS_CA_FILE")) != "" {
			scheme = "https"
			client, err = newAIConsoleTLSClient()
			if err != nil {
				jsonError(w, "AI専用APIのTLSクライアント設定が無効です", http.StatusServiceUnavailable)
				return
			}
		}
		targetURL := scheme + "://" + net.JoinHostPort(aiConsoleRelayHost(), port) + aiPath
		forwardAIConsoleRequest(w, r, targetURL, token, client)
	}
}

func sessionAuditReference(r *http.Request) string {
	session, ok := middleware.SessionFromContext(r.Context())
	if !ok || session == nil || session.ID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(session.ID))
	return hex.EncodeToString(digest[:])
}

type aiConsoleAuditWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *aiConsoleAuditWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *aiConsoleAuditWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func readAIConsoleToken() (string, error) {
	path := strings.TrimSpace(os.Getenv("AI_CONSOLE_TOKEN_FILE"))
	if path == "" {
		return "", fmt.Errorf("AI_CONSOLE_TOKEN_FILE is not configured")
	}
	data, err := secretfile.ReadConfidential(path, 4096)
	if err != nil {
		return "", fmt.Errorf("AI console token could not be read safely: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 43 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("AI console token is invalid")
	}
	return token, nil
}

func newAIConsoleTLSClient() (*http.Client, error) {
	config, err := aitransport.BuildClientTLSConfig(
		os.Getenv("AI_TLS_CA_FILE"),
		os.Getenv("AI_TLS_CLIENT_CERT_FILE"),
		os.Getenv("AI_TLS_CLIENT_KEY_FILE"),
		os.Getenv("AI_TLS_SERVER_NAME"),
	)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = config
	return &http.Client{
		Transport: transport,
		Timeout: 60 * time.Second,
		CheckRedirect: aiConsoleHTTPClient.CheckRedirect,
	}, nil
}

// aiConsoleRelayHost は中継先ホストを返す。AI_HOST_IPが::1などのループバック
// アドレスならそれを尊重し、0.0.0.0等の非ループバック(Docker内待受)や未設定は
// 127.0.0.1へフォールバックする。非ループバックへの転送は行わない。
func aiConsoleRelayHost() string {
	host := strings.TrimSpace(os.Getenv("AI_HOST_IP"))
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return host
	}
	return "127.0.0.1"
}

func forwardAIConsoleRequest(w http.ResponseWriter, r *http.Request, targetURL, token string, client *http.Client) {
	validatedTarget, err := validateAIConsoleTarget(targetURL)
	if err != nil {
		jsonError(w, "AI専用APIの中継先が無効です", http.StatusInternalServerError)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "リクエストの読み取りに失敗しました", http.StatusBadRequest)
		return
	}

	// #nosec G704 -- validatedTarget is restricted to loopback, an explicit
	// port, and one of the two fixed AI endpoint paths.
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, validatedTarget, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "AI専用APIリクエストの作成に失敗しました", http.StatusInternalServerError)
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Omni-AI-Console-Relay", "1")
	for _, value := range r.Header.Values("Idempotency-Key") {
		request.Header.Add("Idempotency-Key", value)
	}

	// #nosec G704 -- validatedTarget passed validateAIConsoleTarget and all
	// clients reject redirects.
	response, err := client.Do(request)
	if err != nil {
		log.Printf("AI console loopback relay failed: %v", err)
		jsonError(w, "AI専用APIへ接続できません", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAIConsoleResponseSize+1))
	if err != nil {
		jsonError(w, "AI専用APIレスポンスの読み取りに失敗しました", http.StatusBadGateway)
		return
	}
	if len(responseBody) > maxAIConsoleResponseSize {
		jsonError(w, "AI専用APIレスポンスが大きすぎます", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if value := response.Header.Get("Idempotency-Replayed"); value != "" {
		w.Header().Set("Idempotency-Replayed", value)
	}
	if value := response.Header.Get("Retry-After"); value != "" {
		w.Header().Set("Retry-After", value)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func isAllowedAIConsolePath(path string) bool {
	return path == "/api/v1/ai/transactions" || path == "/api/v1/ai/analysis"
}

func validateAIConsoleTarget(raw string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") ||
		target.User != nil || target.Opaque != "" || target.RawQuery != "" ||
		target.ForceQuery || target.Fragment != "" || !isAllowedAIConsolePath(target.Path) {
		return "", fmt.Errorf("invalid AI console target")
	}
	ip := net.ParseIP(target.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("AI console target must use a literal loopback IP")
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid AI console target port")
	}
	return target.String(), nil
}
