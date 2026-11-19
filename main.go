//go:build !server || omni_wails_browser

package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
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
	if len(os.Args) > 1 && os.Args[1] == "--self-test-sqlcipher" {
		if len(os.Args) != 2 {
			log.Fatal("--self-test-sqlcipher does not accept additional arguments")
		}
		report, err := runDesktopSQLCipherSelfTest()
		if err != nil {
			log.Fatalf("SQLCipher self-test failed: %v", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			log.Fatalf("encode SQLCipher self-test result: %v", err)
		}
		return
	}

	options, err := desktopApplicationOptions()
	if err != nil {
		log.Fatal(err)
	}
	dataRoot, err := getAppDataRoot()
	if err != nil {
		log.Fatalf("アプリケーションデータディレクトリ取得エラー: %v", err)
	}
	service, err := NewApp(dataRoot)
	if err != nil {
		log.Fatalf("Desktopアカウント初期化エラー: %v", err)
	}
	options.Name = "Omni Money"
	options.Services = []application.Service{application.NewService(service)}
	if options.Assets.Handler == nil {
		options.Assets.Handler = application.BundledAssetFileServer(assets)
	}
	options.Mac.ApplicationShouldTerminateAfterLastWindowClosed = true
	options.Linux.ApplicationID = "com.wails.OmniMoney"
	previousShutdown := options.OnShutdown
	options.OnShutdown = func() {
		if previousShutdown != nil {
			previousShutdown()
		}
		service.shutdown(context.Background())
	}
	app := application.New(options)
	if !wailsBrowserMode {
		app.Window.NewWithOptions(application.WebviewWindowOptions{
			Name: "main", Title: "Omni Money", Width: 1280, Height: 800,
			URL: "/", BackgroundColour: application.NewRGBA(102, 126, 234, 1),
		})
	}
	if err := app.Run(); err != nil {
		log.Fatalf("Wails起動エラー: %v", err)
	}
}
