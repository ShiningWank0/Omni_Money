//go:build !server || omni_wails_browser

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fixedWebviewPathFixture(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	runtimePath := filepath.Join(base, "runtime")
	profile := filepath.Join(base, "profiles", "test")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_TEMP", base)
	t.Setenv("OMNI_CI_WEBVIEW2_RUNTIME", runtimePath)
	t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", profile)
	if err := os.Mkdir(runtimePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimePath, "msedgewebview2.exe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return base, runtimePath, profile
}

func TestFixedCIWebviewPathsCreateOnlyPrivateProfile(t *testing.T) {
	_, wantRuntime, wantProfile := fixedWebviewPathFixture(t)
	runtimePath, profile, err := desktopCIWebviewPaths()
	if err != nil || runtimePath != wantRuntime || profile != wantProfile {
		t.Fatalf("private paths not selected: runtime=%q profile=%q err=%v", runtimePath, profile, err)
	}
	if info, err := os.Stat(profile); err != nil || !info.IsDir() {
		t.Fatal("private profile was not created")
	}
}

func TestFixedCIWebviewPathsRejectIncompleteOrEscapedConfiguration(t *testing.T) {
	for _, name := range []string{"non-CI", "missing-base", "missing-profile", "relative-runtime", "outside-runtime", "outside-profile", "missing-executable"} {
		t.Run(name, func(t *testing.T) {
			base, runtimePath, _ := fixedWebviewPathFixture(t)
			switch name {
			case "non-CI":
				t.Setenv("GITHUB_ACTIONS", "false")
			case "missing-base":
				t.Setenv("RUNNER_TEMP", "")
			case "missing-profile":
				t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", "")
			case "relative-runtime":
				t.Setenv("OMNI_CI_WEBVIEW2_RUNTIME", "runtime")
			case "outside-runtime":
				t.Setenv("OMNI_CI_WEBVIEW2_RUNTIME", filepath.Dir(base))
			case "outside-profile":
				t.Setenv("OMNI_CI_WEBVIEW2_PROFILE", filepath.Dir(base))
			case "missing-executable":
				if err := os.Remove(filepath.Join(runtimePath, "msedgewebview2.exe")); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := desktopCIWebviewPaths(); err == nil {
				t.Fatal("accepted invalid native runtime configuration")
			}
		})
	}
}

func TestFixedCIWebviewPathsRejectEscapingExecutableSymlink(t *testing.T) {
	_, runtimePath, _ := fixedWebviewPathFixture(t)
	executable := filepath.Join(runtimePath, "msedgewebview2.exe")
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.exe")
	if err := os.WriteFile(outside, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, executable); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, _, err := desktopCIWebviewPaths(); err == nil {
		t.Fatal("accepted executable symlink outside the fixed runtime")
	}
}

func TestFixedCIWebviewPathsRejectEscapingProfileSymlink(t *testing.T) {
	_, _, profile := fixedWebviewPathFixture(t)
	if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, profile); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, _, err := desktopCIWebviewPaths(); err == nil {
		t.Fatal("accepted profile symlink outside RUNNER_TEMP")
	}
}

func TestFixedCIWebviewPathsLeaveNormalLaunchUnconfigured(t *testing.T) {
	t.Setenv("OMNI_CI_WEBVIEW2_RUNTIME", "")
	runtimePath, profile, err := desktopCIWebviewPaths()
	if err != nil || runtimePath != "" || profile != "" {
		t.Fatalf("normal launch acquired CI-only settings: %v", err)
	}
}
