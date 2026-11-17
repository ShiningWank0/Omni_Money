//go:build windows && !server

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixedCIWebviewRequiresPrivatePathsAndExecutable(t *testing.T) {
	base := t.TempDir()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_TEMP", base)
	runtimePath := filepath.Join(base, "runtime")
	profile := filepath.Join(base, "profile")
	t.Setenv("OMNI_CI_WEBVIEW2_RUNTIME", runtimePath)
	t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", profile)
	if _, err := desktopCIWebviewOptions(); err == nil {
		t.Fatal("accepted missing fixed runtime")
	}
	if err := os.Mkdir(runtimePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimePath, "msedgewebview2.exe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	options, err := desktopCIWebviewOptions()
	if err != nil || options.WebviewBrowserPath != runtimePath || options.WebviewUserDataPath != profile {
		t.Fatalf("fixed runtime/profile not selected: %v", err)
	}
	t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", filepath.Dir(base))
	if _, err := desktopCIWebviewOptions(); err == nil {
		t.Fatal("accepted profile outside private CI directory")
	}
	t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", profile)
	t.Setenv("GITHUB_ACTIONS", "false")
	if _, err := desktopCIWebviewOptions(); err == nil {
		t.Fatal("accepted CI-only override outside CI")
	}
}
