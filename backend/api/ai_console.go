package api

import (
	"bytes"
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
)

const maxAIConsoleResponseSize = 10 * 1024 * 1024

var aiConsoleHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	// The relay is intentionally loopback-only. Never follow a response to a
	// second destination, even if another local service returns a redirect.
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// handleAIConsoleProxy はセッション認証済みの管理UIから、固定された
// loopback上のAI専用リスナーへリクエストを中継する。URLとBearer tokenは
// ブラウザへ渡さず、任意URLへの転送も許可しない。
func handleAIConsoleProxy(aiPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !isAllowedAIConsolePath(aiPath) {
			jsonError(w, "AI専用APIの中継先が無効です", http.StatusInternalServerError)
			return
		}
		token := strings.TrimSpace(os.Getenv("AI_API_TOKEN"))
		if token == "" {
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

		targetURL := "http://" + net.JoinHostPort(aiConsoleRelayHost(), port) + aiPath
		forwardAIConsoleRequest(w, r, targetURL, token, aiConsoleHTTPClient)
	}
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

	// #nosec G704 -- validatedTarget is restricted to a literal loopback IP,
	// an explicit port, and one of two fixed AI paths below.
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, validatedTarget, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "AI専用APIリクエストの作成に失敗しました", http.StatusInternalServerError)
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	// #nosec G704 -- the URL has passed validateAIConsoleTarget and the
	// production client refuses redirects to any secondary destination.
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
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func isAllowedAIConsolePath(path string) bool {
	return path == "/api/v1/ai/transactions" || path == "/api/v1/ai/analysis"
}

func validateAIConsoleTarget(raw string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "http" || target.User != nil || target.Opaque != "" ||
		target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || !isAllowedAIConsolePath(target.Path) {
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
