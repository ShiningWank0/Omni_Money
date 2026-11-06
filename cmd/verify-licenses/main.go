// verify-licenses checks fixed release-image paths. Its trusted manifest is
// mounted from the frontend builder independently of the runtime files.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

type moduleVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type noticeFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Target             string          `json:"target"`
	ApplicationModule  string          `json:"applicationModule"`
	ApplicationVersion string          `json:"applicationVersion"`
	GOOS               string          `json:"goos"`
	GOARCH             string          `json:"goarch"`
	GoVersion          string          `json:"goVersion"`
	Modules            []moduleVersion `json:"modules"`
	Files              []noticeFile    `json:"files"`
}

func readManifest(reader io.Reader) (manifest, error) {
	var expected manifest
	decoder := json.NewDecoder(io.LimitReader(reader, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&expected); err != nil {
		return manifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return manifest{}, fmt.Errorf("invalid trailing manifest data")
	}
	return expected, nil
}

func verifyBinary(binary io.ReaderAt, expected manifest) error {
	info, err := buildinfo.Read(binary)
	if err != nil {
		return err
	}
	return verifyBuildInfo(info, expected)
}

func verifyBuildInfo(info *debug.BuildInfo, expected manifest) error {
	if expected.ApplicationModule == "" || info.GoVersion != expected.GoVersion {
		return fmt.Errorf("missing application identity or unreviewed Go runtime")
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
		if module.Path == "" || module.Version == "" || modules[module.Path] != "" || module.Path == expected.ApplicationModule {
			return fmt.Errorf("invalid reviewed module inventory")
		}
		modules[module.Path] = module.Version
	}
	if len(modules) == 0 {
		return fmt.Errorf("empty release dependency inventory")
	}
	seen := map[string]bool{}
	for _, module := range info.Deps {
		if module == nil || module.Replace != nil || seen[module.Path] {
			return fmt.Errorf("invalid or replaced release dependency")
		}
		seen[module.Path] = true
		// A ./server.go build records our own module as a (devel) dependency
		// of command-line-arguments. Exclude only that exact application identity.
		if module.Path == expected.ApplicationModule && module.Version == "(devel)" {
			continue
		}
		version, reviewed := modules[module.Path]
		if !reviewed || version != module.Version {
			return fmt.Errorf("unreviewed dependency in release image: %s", module.Path)
		}
		delete(modules, module.Path)
	}
	if len(modules) != 0 {
		return fmt.Errorf("release dependency inventory differs")
	}
	return nil
}

func verify(directory *os.Root, expected manifest, target string) error {
	if expected.Target != target || (target != "server-linux-amd64" && target != "server-linux-arm64") {
		return fmt.Errorf("license target mismatch")
	}
	seen := map[string]bool{}
	for _, file := range expected.Files {
		if (file.Name != "LICENSE" && file.Name != "THIRD_PARTY_NOTICES.txt" && file.Name != "native/SQLCipher-LICENSE.txt") || seen[file.Name] {
			return fmt.Errorf("invalid license inventory")
		}
		seen[file.Name] = true
		info, err := directory.Lstat(file.Name)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing regular license file: %s", file.Name)
		}
		content, err := directory.ReadFile(file.Name)
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

func run(target string) error {
	licenses, err := os.OpenRoot("/app/licenses")
	if err != nil {
		return err
	}
	defer licenses.Close()
	trusted, err := os.OpenRoot("/tmp")
	if err != nil {
		return err
	}
	defer trusted.Close()
	file, err := trusted.Open("license-manifest.json")
	if err != nil {
		return err
	}
	defer file.Close()
	expected, err := readManifest(file)
	if err != nil {
		return err
	}
	if err := verify(licenses, expected, target); err != nil {
		return err
	}
	app, err := os.OpenRoot("/app")
	if err != nil {
		return err
	}
	defer app.Close()
	binary, err := app.Open("omni_money_server")
	if err != nil {
		return err
	}
	defer binary.Close()
	return verifyBinary(binary, expected)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: verify-licenses TARGET")
		os.Exit(1)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
