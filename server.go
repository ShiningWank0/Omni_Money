//go:build server

package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"omni_money/backend/api"
	"omni_money/backend/database"
)

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

func main() {
	passwordHash := strings.TrimSpace(os.Getenv("AUTH_PASSWORD_HASH"))
	if passwordHash == "" {
		log.Fatal("AUTH_PASSWORD_HASH が未設定です（サーバーモードでは必須）")
	}
	if _, err := bcrypt.Cost([]byte(passwordHash)); err != nil {
		log.Fatal("AUTH_PASSWORD_HASH が有効なbcryptハッシュではありません")
	}

	// データベースの初期化
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "omni_money.db"
	}
	if err := database.InitDB(dbPath); err != nil {
		log.Fatalf("データベース初期化エラー: %v", err)
	}
	defer database.CloseDB()

	// 公開Web用ホストIPとポートの設定
	host := os.Getenv("HOST_IP")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}

	addr := net.JoinHostPort(host, port)
	certFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	if (certFile == "") != (keyFile == "") {
		log.Fatal("TLS_CERT_FILE と TLS_KEY_FILE は両方指定してください")
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      api.NewRouter(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		if certFile != "" {
			log.Printf("Omni Money v%s 公開Web起動 (TLS): %s", version, addr)
			errCh <- srv.ListenAndServeTLS(certFile, keyFile)
			return
		}
		log.Printf("Omni Money v%s 公開Web起動 (HTTP): %s", version, addr)
		errCh <- srv.ListenAndServe()
	}()

	// AI APIは別リスナーで提供する。トークン未設定時はリスナー自体を起動しない。
	aiToken := strings.TrimSpace(os.Getenv("AI_API_TOKEN"))
	if aiToken != "" {
		if len(aiToken) < 32 {
			log.Fatal("AI_API_TOKEN は32文字以上のランダムな値を設定してください")
		}

		aiHost := strings.TrimSpace(os.Getenv("AI_HOST_IP"))
		if aiHost == "" {
			aiHost = "127.0.0.1"
		}
		aiPort := strings.TrimSpace(os.Getenv("AI_PORT"))
		if aiPort == "" {
			aiPort = "4001"
		}
		allowRemoteAI := strings.EqualFold(strings.TrimSpace(os.Getenv("AI_ALLOW_REMOTE")), "true")
		if !isLoopbackHost(aiHost) && !allowRemoteAI {
			log.Fatal("AI_HOST_IP がループバック以外です。Dockerのlocalhost限定ポート公開などを確認し、明示的に AI_ALLOW_REMOTE=true を設定してください")
		}

		aiAddr := net.JoinHostPort(aiHost, aiPort)
		aiServer := &http.Server{
			Addr:         aiAddr,
			Handler:      api.NewAIRouter(aiToken),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("Omni Money v%s AI専用API起動: %s", version, aiAddr)
			errCh <- aiServer.ListenAndServe()
		}()
	} else {
		log.Printf("AI_API_TOKEN 未設定のためAI専用APIは無効です")
	}

	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("サーバー停止: %v", err)
	}
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
