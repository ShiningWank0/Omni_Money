package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
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
	expected, err := readManifest(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := verify(root, expected, "server-linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if err := verify(root, expected, "server-linux-arm64"); err == nil {
		t.Fatal("wrong platform accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "THIRD_PARTY_NOTICES.txt"), []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(root, expected, "server-linux-amd64"); err == nil {
		t.Fatal("changed notices accepted")
	}
	if err := os.Remove(filepath.Join(dir, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := verify(root, expected, "server-linux-amd64"); err == nil {
		t.Fatal("missing license accepted")
	}
}

func TestVerifyBuiltDependencyVersionsAndPlatform(t *testing.T) {
	expected := manifest{ApplicationModule: "example.invalid/app", GoVersion: "go-fixture", GOOS: "linux", GOARCH: "amd64", Modules: []moduleVersion{{Path: "example.invalid/library", Version: "v1.0.0"}}}
	fixture := debug.BuildInfo{GoVersion: expected.GoVersion, Deps: []*debug.Module{{Path: expected.Modules[0].Path, Version: expected.Modules[0].Version}}, Settings: []debug.BuildSetting{{Key: "GOOS", Value: expected.GOOS}, {Key: "GOARCH", Value: expected.GOARCH}}}
	if err := verifyBuildInfo(&fixture, expected); err != nil {
		t.Fatal(err)
	}
	fixture.Deps = append(fixture.Deps, &debug.Module{Path: expected.ApplicationModule, Version: "(devel)"})
	if err := verifyBuildInfo(&fixture, expected); err != nil {
		t.Fatalf("single-file builds record the application's own module: %v", err)
	}
	for name, mutate := range map[string]func(*debug.BuildInfo){
		"wrong runtime":            func(info *debug.BuildInfo) { info.GoVersion = "different" },
		"wrong CPU":                func(info *debug.BuildInfo) { info.Settings[1].Value = "different" },
		"missing dependency":       func(info *debug.BuildInfo) { info.Deps = nil },
		"wrong dependency version": func(info *debug.BuildInfo) { info.Deps[0].Version = "different" },
		"additional dependency": func(info *debug.BuildInfo) {
			info.Deps = append(info.Deps, &debug.Module{Path: "extra.invalid/library", Version: "v1.0.0"})
		},
		"unversioned additional dependency": func(info *debug.BuildInfo) {
			info.Deps = append(info.Deps, &debug.Module{Path: "extra.invalid/library"})
		},
		"replacement dependency":           func(info *debug.BuildInfo) { info.Deps[0].Replace = &debug.Module{Path: "local"} },
		"versioned application dependency": func(info *debug.BuildInfo) { info.Deps[1].Version = "v1.0.0" },
		"replaced application dependency":  func(info *debug.BuildInfo) { info.Deps[1].Replace = &debug.Module{Path: "local"} },
		"different local module":           func(info *debug.BuildInfo) { info.Deps[1].Path = "another.invalid/app" },
		"duplicate module":                 func(info *debug.BuildInfo) { info.Deps = append(info.Deps, info.Deps[0]) },
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

func TestLicenseInventoryRejectsEscapingSymlinksAndNames(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SQLCipher-LICENSE.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "native")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"native/SQLCipher-LICENSE.txt", "../outside", "/outside"} {
		expected := manifest{Target: "server-linux-amd64", Files: []noticeFile{{Name: name}}}
		if err := verify(root, expected, expected.Target); err == nil {
			t.Fatalf("escaping inventory accepted: %s", name)
		}
	}
}

func TestManifestRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	for _, input := range []string{`{"unexpected":true}`, `{} {}`, `{} garbage`} {
		if _, err := readManifest(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid manifest accepted: %s", input)
		}
	}
}
