package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"omni_money/backend/database"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// データベースの初期化
	if err := database.InitDB(""); err != nil {
		log.Fatalf("データベース初期化エラー: %v", err)
	}
	defer database.CloseDB()

	// アプリケーション構造体を作成
	app := NewApp()

	// Wailsアプリケーションを起動
	err := wails.Run(&options.App{
		Title:  "Omni Money",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 102, G: 126, B: 234, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Fatalf("Wails起動エラー: %v", err)
	}
}
