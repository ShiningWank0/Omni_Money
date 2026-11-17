//go:build !server || omni_wails_browser

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The Go WebView2 loader clears standard WEBVIEW2_* overrides during init.
// Select the already verified CI runtime through Wails' explicit option.
func desktopCIWebviewOptions() (*application.WindowsOptions, error) {
	runtimePath := os.Getenv("OMNI_CI_WEBVIEW2_RUNTIME")
	if runtimePath == "" || runtime.GOOS != "windows" {
		return nil, nil
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return nil, fmt.Errorf("fixed test WebView2 requires CI")
	}
	profile := os.Getenv("OMNI_CI_WEBVIEW2_PROFILE")
	for _, path := range []string{runtimePath, profile} {
		base := os.Getenv("RUNNER_TEMP")
		rel, err := filepath.Rel(base, path)
		if base == "" || !filepath.IsAbs(path) || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("test WebView2 paths must be inside RUNNER_TEMP")
		}
	}
	if info, err := os.Stat(filepath.Join(runtimePath, "msedgewebview2.exe")); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("fixed WebView2 executable is missing")
	}
	return &application.WindowsOptions{WebviewBrowserPath: runtimePath, WebviewUserDataPath: profile}, nil
}
