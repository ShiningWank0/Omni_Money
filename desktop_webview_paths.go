//go:build !server || omni_wails_browser

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Runtime/profile overrides apply only to an explicit CI native launch. Use
// directory-relative operations so symlinks cannot escape RUNNER_TEMP.
func desktopCIWebviewPaths() (string, string, error) {
	runtimePath := os.Getenv("OMNI_CI_WEBVIEW2_RUNTIME")
	if runtimePath == "" {
		return "", "", nil
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return "", "", fmt.Errorf("fixed test WebView2 requires CI")
	}
	base := os.Getenv("RUNNER_TEMP")
	if !filepath.IsAbs(base) {
		return "", "", fmt.Errorf("test WebView2 requires an absolute RUNNER_TEMP")
	}
	profile := os.Getenv("OMNI_CI_WEBVIEW2_PROFILE")
	relativePaths := make([]string, 0, 2)
	for _, path := range []string{runtimePath, profile} {
		rel, err := filepath.Rel(base, path)
		if !filepath.IsAbs(path) || err != nil || rel == "." || !filepath.IsLocal(rel) {
			return "", "", fmt.Errorf("test WebView2 paths must be inside RUNNER_TEMP")
		}
		relativePaths = append(relativePaths, rel)
	}
	sandbox, err := os.OpenRoot(base)
	if err != nil {
		return "", "", fmt.Errorf("open CI directory: %w", err)
	}
	defer sandbox.Close()
	runtimeRoot, err := sandbox.OpenRoot(relativePaths[0])
	if err != nil {
		return "", "", fmt.Errorf("open fixed WebView2 directory: %w", err)
	}
	defer runtimeRoot.Close()
	if info, err := runtimeRoot.Stat("msedgewebview2.exe"); err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("fixed WebView2 executable is missing or outside its directory")
	}
	if err := sandbox.MkdirAll(relativePaths[1], 0700); err != nil {
		return "", "", fmt.Errorf("create private WebView2 profile: %w", err)
	}
	profileRoot, err := sandbox.OpenRoot(relativePaths[1])
	if err != nil {
		return "", "", fmt.Errorf("open private WebView2 profile: %w", err)
	}
	if err := profileRoot.Close(); err != nil {
		return "", "", err
	}
	return filepath.Join(base, relativePaths[0]), filepath.Join(base, relativePaths[1]), nil
}
