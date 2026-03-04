package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"omni_money/backend/database"
)

//go:embed all:frontend/dist
var assets embed.FS

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

// getAppDataDBPath はOS標準のアプリケーションデータディレクトリ内のDBパスを返す。
// Finder/open コマンドで起動するとcwdが "/" になるため、相対パスは使えない。
func getAppDataDBPath() string {
	var baseDir string
	switch runtime.GOOS {
	case "darwin":
		// macOS: ~/Library/Application Support/OmniMoney/
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Printf("ホームディレクトリ取得失敗、カレントディレクトリにフォールバック: %v", err)
			return "omni_money.db"
		}
		baseDir = filepath.Join(homeDir, "Library", "Application Support", "OmniMoney")
	case "windows":
		// Windows: %APPDATA%/OmniMoney/
		appData := os.Getenv("APPDATA")
		if appData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "omni_money.db"
			}
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		baseDir = filepath.Join(appData, "OmniMoney")
	default:
		// Linux: ~/.local/share/OmniMoney/
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "omni_money.db"
			}
			dataHome = filepath.Join(homeDir, ".local", "share")
		}
		baseDir = filepath.Join(dataHome, "OmniMoney")
	}
	return filepath.Join(baseDir, "omni_money.db")
}

func main() {
	// デスクトップアプリ用: OS標準のアプリケーションデータディレクトリにDBを保存
	// Finder/open起動時はcwdが "/" になるため、相対パスは使えない
	dbPath := getAppDataDBPath()
	if err := database.InitDB(dbPath); err != nil {
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
