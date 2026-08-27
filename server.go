//go:build server

package main

import (
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"omni_money/backend/aicredentials"
	"omni_money/backend/aitransport"
	"omni_money/backend/api"
	"omni_money/backend/audithmac"
	"omni_money/backend/config"
	"omni_money/backend/database"
	"omni_money/backend/middleware"
)

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

func main() {
	passwordHash := strings.TrimSpace(os.Getenv("AUTH_PASSWORD_HASH"))
	if passwordHash == "" {
		log.Fatal("AUTH_PASSWORD_HASH が未設定です（サーバーモードでは必須）")
	}
	if err := middleware.ValidatePasswordHash(passwordHash); err != nil {
		log.Fatalf("AUTH_PASSWORD_HASH の安全性検証に失敗しました: %v", err)
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

	publicHandler, err := api.NewRouterWithError()
	if err != nil {
		log.Fatalf("公開Webのセキュリティ設定が無効です: %v", err)
	}
	if err := middleware.ValidatePublicListenerSecurity(host, certFile != ""); err != nil {
		log.Fatalf("公開Webの待受設定が安全ではありません: %v", err)
	}
	if certFile == "" && !config.IsLoopbackHost(host) && strings.TrimSpace(os.Getenv("ALLOW_INSECURE_HTTP")) == "true" {
		log.Printf("警告: ALLOW_INSECURE_HTTP=true により非loopback HTTP待受を許可します。Dockerのhost公開先を127.0.0.1に限定してください")
	}
	publicTLSConfig, err := aitransport.BuildPublicServerTLSConfig(certFile, keyFile)
	if err != nil {
		log.Fatalf("公開WebのTLS設定が無効です: %v", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           publicHandler,
		TLSConfig:         publicTLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		if certFile != "" {
			log.Printf("Omni Money v%s 公開Web起動 (TLS): %s", version, addr)
			errCh <- srv.ListenAndServeTLS("", "")
			return
		}
		log.Printf("Omni Money v%s 公開Web起動 (HTTP): %s", version, addr)
		errCh <- srv.ListenAndServe()
	}()

	// AI APIは別リスナーで提供する。資格情報ファイル未設定時はリスナー自体を起動しない。
	if strings.TrimSpace(os.Getenv("AI_API_TOKEN")) != "" {
		log.Fatal("AI_API_TOKEN は廃止されました。期限・権限・口座制約を持つ AI_CREDENTIALS_FILE を使用してください")
	}
	aiCredentialsFile := strings.TrimSpace(os.Getenv("AI_CREDENTIALS_FILE"))
	if aiCredentialsFile != "" {
		credentialStore, err := aicredentials.NewStore(aiCredentialsFile)
		if err != nil {
			log.Fatalf("AI資格情報ファイルが無効です: %v", err)
		}
		auditKeyringFile := strings.TrimSpace(os.Getenv("AI_AUDIT_HMAC_KEYRING_FILE"))
		if auditKeyringFile == "" {
			log.Fatal("AI_AUDIT_HMAC_KEYRING_FILE が未設定です（AI API有効時は専用監査鍵が必須です）")
		}
		auditStore, err := audithmac.NewStore(auditKeyringFile)
		if err != nil {
			log.Fatalf("AI監査HMAC keyringが無効です: %v", err)
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
			log.Fatal("AI_HOST_IP がループバック以外です。mTLS設定を確認し、明示的に AI_ALLOW_REMOTE=true を設定してください")
		}

		aiAddr := net.JoinHostPort(aiHost, aiPort)
		aiTLSConfig, err := aitransport.BuildServerTLSConfig(
			aiHost,
			os.Getenv("AI_TLS_CERT_FILE"),
			os.Getenv("AI_TLS_KEY_FILE"),
			os.Getenv("AI_TLS_CLIENT_CA_FILE"),
		)
		if err != nil {
			log.Fatalf("AI専用APIのTLS設定が無効です: %v", err)
		}
		aiServer := &http.Server{
			Addr:              aiAddr,
			Handler:           api.NewAIRouter(credentialStore, auditStore),
			TLSConfig:         aiTLSConfig,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		watchAIConfigReload(credentialStore, auditStore)
		go func() {
			listener, err := net.Listen("tcp", aiAddr)
			if err != nil {
				errCh <- err
				return
			}
			if aiTLSConfig != nil {
				transportLabel := "TLS 1.3"
				if aiTLSConfig.ClientAuth == tls.RequireAndVerifyClientCert {
					transportLabel += "/mTLS"
				}
				log.Printf("Omni Money v%s AI専用API起動 (%s): %s", version, transportLabel, aiAddr)
				listener = tls.NewListener(listener, aiTLSConfig)
			} else {
				log.Printf("Omni Money v%s AI専用API起動 (loopback HTTP): %s", version, aiAddr)
			}
			errCh <- aiServer.Serve(listener)
		}()
	} else {
		log.Printf("AI_CREDENTIALS_FILE 未設定のためAI専用APIは無効です")
	}

	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("サーバー停止: %v", err)
	}
}

func watchAIConfigReload(credentialStore *aicredentials.Store, auditStore *audithmac.Store) {
	reload := make(chan os.Signal, 1)
	// Signal 1 is SIGHUP on the Unix platforms used by the server image. Using
	// the numeric syscall.Signal keeps the server source buildable on Windows;
	// Windows operators restart the process after atomic credential replacement.
	signal.Notify(reload, syscall.Signal(1))
	go func() {
		for range reload {
			if err := credentialStore.Reload(); err != nil {
				// Reload is atomic: an invalid replacement never displaces the
				// last valid snapshot. Do not log credential contents.
				log.Printf("AI資格情報の再読込を拒否しました。直前の有効な設定を維持します: %v", err)
			} else {
				log.Printf("AI資格情報を安全に再読込しました (%d件)", len(credentialStore.List()))
			}
			if err := auditStore.Reload(); err != nil {
				log.Printf("AI監査HMAC keyringの再読込を拒否しました。直前の有効な鍵を維持します: %v", err)
			} else {
				log.Printf("AI監査HMAC keyringを安全に再読込しました (key_id=%s)", auditStore.CurrentKeyID())
			}
		}
	}()
}
