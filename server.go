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
	"omni_money/backend/config"
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
		host = "127.0.0.1"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}

	addr := net.JoinHostPort(host, port)
	certFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	allowInsecureHTTP := strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_INSECURE_HTTP")), "true")
	transportConfig := config.WebTransportConfig{
		ListenHost:        host,
		ExternalHost:      os.Getenv("WEB_EXTERNAL_HOST"),
		TLSCertFile:       certFile,
		TLSKeyFile:        keyFile,
		ForceHTTPS:        strings.EqualFold(strings.TrimSpace(os.Getenv("FORCE_HTTPS")), "true"),
		TrustedProxies:    os.Getenv("TRUSTED_PROXIES"),
		AllowInsecureHTTP: allowInsecureHTTP,
	}
	if err := config.ValidateWebTransport(transportConfig); err != nil {
		log.Fatalf("公開Web設定エラー: %v", err)
	}
	if certFile == "" && allowInsecureHTTP {
		log.Printf("警告: ALLOW_INSECURE_HTTP=true により平文HTTPの外部公開を許可しています。認証情報と財務データは暗号化されません")
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
		if !config.IsLoopbackHost(aiHost) && !allowRemoteAI {
			log.Fatal("AI_HOST_IP がループバック以外です。Dockerのlocalhost限定ポート公開などを確認し、明示的に AI_ALLOW_REMOTE=true を設定してください")
		}

		aiAddr := net.JoinHostPort(aiHost, aiPort)
		if !config.IsLoopbackHost(aiHost) {
			log.Printf("警告: AI専用APIを非ループバックアドレス %s にTLSなしで公開します。BearerトークンとAI送受信データが平文で流れるため、Dockerのlocalhost限定ポート公開やリバースプロキシでのTLS終端で必ず保護してください", aiAddr)
		}
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
