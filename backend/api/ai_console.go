package api

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxAIConsoleResponseSize = 10 * 1024 * 1024

var aiConsoleHTTPClient = &http.Client{Timeout: 60 * time.Second}

// handleAIConsoleProxy はセッション認証済みの管理UIから、固定された
// loopback上のAI専用リスナーへリクエストを中継する。URLとBearer tokenは
// ブラウザへ渡さず、任意URLへの転送も許可しない。
func handleAIConsoleProxy(aiPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
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

		targetURL := "http://" + net.JoinHostPort("127.0.0.1", port) + aiPath
		forwardAIConsoleRequest(w, r, targetURL, token, aiConsoleHTTPClient)
	}
}

func forwardAIConsoleRequest(w http.ResponseWriter, r *http.Request, targetURL, token string, client *http.Client) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "リクエストの読み取りに失敗しました", http.StatusBadRequest)
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "AI専用APIリクエストの作成に失敗しました", http.StatusInternalServerError)
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		jsonError(w, fmt.Sprintf("AI専用APIへ接続できません: %v", err), http.StatusBadGateway)
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
