//go:build server

package main

import (
	"log"
	"net/http"
	"os"
	"strings"

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

	if certFile != "" {
		log.Printf("Omni Money v%s サーバーモード起動 (TLS): %s", version, addr)
		if err := http.ListenAndServeTLS(addr, certFile, keyFile, router); err != nil {
			log.Fatalf("TLSサーバー停止: %v", err)
		}
		return
	}

	log.Printf("Omni Money v%s サーバーモード起動 (HTTP): %s", version, addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("サーバー停止: %v", err)
	}
}
