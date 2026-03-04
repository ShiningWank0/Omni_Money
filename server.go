//go:build server

package main

import (
	"log"
	"net/http"
	"os"

	"omni_money/backend/api"
	"omni_money/backend/database"
)

func main() {
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
	log.Printf("Omni Money サーバーモード起動: %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("サーバー停止: %v", err)
	}
}
