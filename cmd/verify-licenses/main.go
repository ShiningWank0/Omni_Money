// verify-licenses verifies the actual notices copied into a release image.
// Its trusted manifest is mounted from the frontend builder, not read from
// the runtime directory being verified.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

type moduleVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type manifest struct {
	Target    string          `json:"target"`
	GOOS      string          `json:"goos"`
	GOARCH    string          `json:"goarch"`
	GoVersion string          `json:"goVersion"`
	Modules   []moduleVersion `json:"modules"`
	Files     []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func verifyBinary(binary, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var expected manifest
	if err := json.Unmarshal(data, &expected); err != nil {
		return err
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return err
	}
	return verifyBuildInfo(info, expected)
}

func verifyBuildInfo(info *debug.BuildInfo, expected manifest) error {
	if info.GoVersion != expected.GoVersion {
		return fmt.Errorf("unreviewed Go runtime in release image")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != expected.GOOS || settings["GOARCH"] != expected.GOARCH {
		return fmt.Errorf("release binary platform differs from license inventory")
	}
	modules := map[string]string{}
	for _, module := range expected.Modules {
		modules[module.Path] = module.Version
	}
	if len(modules) == 0 || len(modules) != len(info.Deps) {
		return fmt.Errorf("release dependency inventory differs")
	}
	for _, module := range info.Deps {
		if module.Replace != nil || modules[module.Path] != module.Version {
			return fmt.Errorf("unreviewed dependency in release image: %s", module.Path)
		}
	}
	return nil
}

func verify(directory, manifestPath, target string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var expected manifest
	if err := json.Unmarshal(data, &expected); err != nil {
		return err
	}
	if expected.Target != target || !strings.HasPrefix(target, "server-linux-") {
		return fmt.Errorf("license target mismatch")
	}
	seen := map[string]bool{}
	for _, file := range expected.Files {
		if (file.Name != "LICENSE" && file.Name != "THIRD_PARTY_NOTICES.txt" && file.Name != "native/SQLCipher-LICENSE.txt") || seen[file.Name] {
			return fmt.Errorf("invalid license inventory")
		}
		seen[file.Name] = true
		filename := filepath.Join(directory, file.Name)
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing regular license file: %s", file.Name)
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		if len(content) == 0 || hex.EncodeToString(digest[:]) != file.SHA256 {
			return fmt.Errorf("license hash mismatch: %s", file.Name)
		}
	}
	if len(seen) != 3 {
		return fmt.Errorf("incomplete license inventory")
	}
	return nil
}

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: verify-licenses DIRECTORY TRUSTED-MANIFEST TARGET BINARY")
		os.Exit(1)
	}
	if err := verify(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := verifyBinary(os.Args[4], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
