//go:build !server || omni_wails_browser

package main

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The Go WebView2 loader clears standard WEBVIEW2_* overrides during init.
// Select the already verified CI runtime through Wails' explicit option.
func desktopCIWebviewOptions(bindingGeneration bool) (*application.WindowsOptions, error) {
	// Wails 2 executes main while generating bindings. It must not inspect
	// runtime files or require the native UI test's private profile.
	if bindingGeneration || runtime.GOOS != "windows" {
		return nil, nil
	}
	runtimePath, profile, err := desktopCIWebviewPaths()
	if err != nil || runtimePath == "" {
		return nil, err
	}
	return &application.WindowsOptions{WebviewBrowserPath: runtimePath, WebviewUserDataPath: profile}, nil
}
