package main

import (
	"go/build"
	"testing"
)

// Server scans and package builds must select the same headless entrypoint.
// Desktop vault tests remain enabled for native and browser diagnostic builds.
func TestWailsBuildConstraints(t *testing.T) {
	for _, mode := range []struct {
		name             string
		tags             []string
		app, defaultHost bool
	}{
		{"desktop", nil, true, true},
		{"server", []string{"server"}, false, false},
		{"browser", []string{"server", "omni_wails_browser"}, true, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			context := build.Default
			context.BuildTags = mode.tags
			for file, want := range map[string]bool{
				"app.go":                          mode.app,
				"app_desktop_vault_test.go":       mode.app,
				"desktop_browser_host_default.go": mode.defaultHost,
			} {
				got, err := context.MatchFile(".", file)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s selected=%t, want %t", file, got, want)
				}
			}
		})
	}
}
