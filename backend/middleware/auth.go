// Package middleware は認証、AI用APIの接続制御を提供する
package middleware

import (
	"net/http"
	"strings"
)

// AIWriteOnlyMiddleware はAI用APIの書き込み専用制御ミドルウェア
// AI用の認証鍵（トークン）を検証し、POSTのみを許可する
func AIWriteOnlyMiddleware(apiToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// AI用エンドポイントのみチェック
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ai/") {
			next.ServeHTTP(w, r)
			return
		}

		// トークン検証
		token := r.Header.Get("Authorization")
		expectedToken := "Bearer " + apiToken
		if token != expectedToken {
			http.Error(w, `{"error":"認証が必要です"}`, http.StatusUnauthorized)
			return
		}

		// POSTのみ許可
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"AI用APIは新規追加(POST)のみ許可されています"}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
