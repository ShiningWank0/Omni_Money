//go:build server

package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"omni_money/backend/api"
	"omni_money/backend/database"
)

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

func main() {
	if strings.TrimSpace(os.Getenv("AUTH_PASSWORD_HASH")) == "" {
		log.Fatal("AUTH_PASSWORD_HASH が未設定です（サーバーモードでは必須）")
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

	// APIルーターを取得
	router := api.NewRouter()

	// ホストIPとポートの設定
	host := os.Getenv("HOST_IP")
	if host == "" {
		host = "0.0.0.0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}

	addr := host + ":" + port
	certFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	if (certFile == "") != (keyFile == "") {
		log.Fatal("TLS_CERT_FILE と TLS_KEY_FILE は両方指定してください")
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if certFile != "" {
		log.Printf("Omni Money v%s サーバーモード起動 (TLS): %s", version, addr)
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil {
			log.Fatalf("TLSサーバー停止: %v", err)
		}
		return
	}

	log.Printf("Omni Money v%s サーバーモード起動 (HTTP): %s", version, addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("サーバー停止: %v", err)
	}
}
