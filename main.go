//go:build !server

package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// version はCI/CDビルド時に -ldflags で埋め込まれる（§8.3準拠）
var version = "dev"

// getAppDataRoot はOS標準のアプリケーションデータディレクトリを返す。
// Finder/open コマンドで起動するとcwdが "/" になるため、相対パスは使えない。
func getAppDataRoot() (string, error) {
	var baseDir string
	switch runtime.GOOS {
	case "darwin":
		// macOS: ~/Library/Application Support/OmniMoney/
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("ホームディレクトリ取得失敗: %w", err)
		}
		baseDir = filepath.Join(homeDir, "Library", "Application Support", "OmniMoney")
	case "windows":
		// Windows: %APPDATA%/OmniMoney/
		appData := os.Getenv("APPDATA")
		if appData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("ホームディレクトリ取得失敗: %w", err)
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
				return "", fmt.Errorf("ホームディレクトリ取得失敗: %w", err)
			}
			dataHome = filepath.Join(homeDir, ".local", "share")
		}
		baseDir = filepath.Join(dataHome, "OmniMoney")
	}
	absolute, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("アプリケーションデータディレクトリ解決失敗: %w", err)
	}
	return absolute, nil
}

func main() {
	dataRoot, err := getAppDataRoot()
	if err != nil {
		log.Fatalf("アプリケーションデータディレクトリ取得エラー: %v", err)
	}

	// Coordinator construction reads only public account metadata. The
	// SQLCipher vault remains closed until the user explicitly unlocks it.
	app, err := NewApp(dataRoot)
	if err != nil {
		log.Fatalf("Desktopアカウント初期化エラー: %v", err)
	}

	// Wailsアプリケーションを起動
	err = wails.Run(&options.App{
		Title:  "Omni Money",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 102, G: 126, B: 234, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Fatalf("Wails起動エラー: %v", err)
	}
}
