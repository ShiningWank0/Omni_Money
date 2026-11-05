package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestVerifyLicenseBundle(t *testing.T) {
	dir := t.TempDir()
	files := []map[string]string{}
	if err := os.Mkdir(filepath.Join(dir, "native"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.txt", "native/SQLCipher-LICENSE.txt"} {
		content := []byte("reviewed " + name)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		files = append(files, map[string]string{"name": name, "sha256": hex.EncodeToString(digest[:])})
	}
	data, _ := json.Marshal(map[string]any{"target": "server-linux-amd64", "files": files})
	manifestPath := filepath.Join(t.TempDir(), "trusted.json")
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir, manifestPath, "server-linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir, manifestPath, "server-linux-arm64"); err == nil {
		t.Fatal("wrong platform accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "THIRD_PARTY_NOTICES.txt"), []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir, manifestPath, "server-linux-amd64"); err == nil {
		t.Fatal("changed notices accepted")
	}
	if err := os.Remove(filepath.Join(dir, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := verify(dir, manifestPath, "server-linux-amd64"); err == nil {
		t.Fatal("missing license accepted")
	}
}

func TestVerifyBuiltDependencyVersionsAndPlatform(t *testing.T) {
	expected := manifest{GoVersion: "go-fixture", GOOS: "linux", GOARCH: "amd64", Modules: []moduleVersion{{Path: "example.invalid/library", Version: "v1.0.0"}}}
	fixture := debug.BuildInfo{GoVersion: expected.GoVersion, Deps: []*debug.Module{{Path: expected.Modules[0].Path, Version: expected.Modules[0].Version}}, Settings: []debug.BuildSetting{{Key: "GOOS", Value: expected.GOOS}, {Key: "GOARCH", Value: expected.GOARCH}}}
	if err := verifyBuildInfo(&fixture, expected); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*debug.BuildInfo){
		"wrong runtime":            func(info *debug.BuildInfo) { info.GoVersion = "different" },
		"wrong CPU":                func(info *debug.BuildInfo) { info.Settings[1].Value = "different" },
		"missing dependency":       func(info *debug.BuildInfo) { info.Deps = nil },
		"wrong dependency version": func(info *debug.BuildInfo) { info.Deps[0].Version = "different" },
		"additional dependency": func(info *debug.BuildInfo) {
			info.Deps = append(info.Deps, &debug.Module{Path: "extra.invalid/library", Version: "v1.0.0"})
		},
		"replacement dependency": func(info *debug.BuildInfo) { info.Deps[0].Replace = &debug.Module{Path: "local"} },
	} {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(fixture)
			var candidate debug.BuildInfo
			if err := json.Unmarshal(data, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if err := verifyBuildInfo(&candidate, expected); err == nil {
				t.Fatal("unreviewed build accepted")
			}
		})
	}
}
