// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"encoding/json"
	"net/http"
	"strings"
)

// AIAPIMiddleware はAI用APIの接続制御ミドルウェア
// AI用の認証鍵（トークン）を検証し、POSTのみを許可する。
// Agent.md §6.3: 変更（PUT）・削除（DELETE）が要求された場合はHTTP 403で遮断。
// 許可パス: POST /api/v1/ai/transactions, POST /api/v1/ai/analysis
func AIAPIMiddleware(apiToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// AI用エンドポイントのみチェック
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ai/") {
			next.ServeHTTP(w, r)
			return
		}

		// トークン未設定の場合は拒否
		if apiToken == "" {
			writeJSONError(w, "AI用APIトークンが設定されていません", http.StatusUnauthorized)
			return
		}

		// トークン検証
		token := r.Header.Get("Authorization")
		expectedToken := "Bearer " + apiToken
		if token != expectedToken {
			writeJSONError(w, "認証が必要です", http.StatusUnauthorized)
			return
		}

		// POSTのみ許可。GET/PUT/DELETE等はHTTP 403で即座に遮断（Agent.md §6.3）
		if r.Method != http.MethodPost {
			writeJSONError(w, "AI用APIは新規追加(POST)と分析(POST)のみ許可されています", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// writeJSONError はJSON形式のエラーレスポンスを返す
// Content-Type: application/json を設定し、構造化されたエラーを返す
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
