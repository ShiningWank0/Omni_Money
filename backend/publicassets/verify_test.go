package publicassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func manifestFixture(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	manifest := Manifest{Version: 1}
	for name, body := range files {
		writeFixture(t, filepath.Join(root, filepath.FromSlash(name)), body)
		digest := sha256.Sum256([]byte(body))
		manifest.Files = append(manifest.Files, AssetHash{Name: name, SHA256: hex.EncodeToString(digest[:])})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "public-assets-manifest.json")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestVerifyRejectsContaminatedPublicOutputs(t *testing.T) {
	for _, scenario := range []string{"clean", "env", "db", "key", "backup", "source-map", "unknown-svg", "extra-directory", "changed", "missing", "file-link", "directory-link", "root-link", "manifest-link"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			manifest := manifestFixture(t, root, map[string]string{"index.html": "<html></html>", "assets/app.js": "public", "assets/icon.svg": "<svg/>"})
			switch scenario {
			case "env":
				writeFixture(t, filepath.Join(root, ".env"), "private-marker")
			case "db":
				writeFixture(t, filepath.Join(root, "assets", "control.db"), "private-marker")
			case "key":
				writeFixture(t, filepath.Join(root, "assets", "control.key"), "private-marker")
			case "backup":
				writeFixture(t, filepath.Join(root, "data.tar"), "private-marker")
			case "source-map":
				writeFixture(t, filepath.Join(root, "assets", "app.js.map"), "private-marker")
			case "unknown-svg":
				writeFixture(t, filepath.Join(root, "assets", "unexpected.svg"), "private-marker")
			case "extra-directory":
				if err := os.Mkdir(filepath.Join(root, "secrets"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "changed":
				writeFixture(t, filepath.Join(root, "assets", "app.js"), "private-marker")
			case "missing":
				if err := os.Remove(filepath.Join(root, "assets", "app.js")); err != nil {
					t.Fatal(err)
				}
			case "file-link":
				if err := os.Remove(filepath.Join(root, "assets", "icon.svg")); err != nil {
					t.Fatal(err)
				}
				requireLink(t, "app.js", filepath.Join(root, "assets", "icon.svg"))
			case "directory-link":
				if err := os.Rename(filepath.Join(root, "assets"), filepath.Join(root, "actual")); err != nil {
					t.Fatal(err)
				}
				requireLink(t, "actual", filepath.Join(root, "assets"))
			case "root-link":
				alias := filepath.Join(t.TempDir(), "root-link")
				requireLink(t, root, alias)
				root = alias
			case "manifest-link":
				alias := filepath.Join(t.TempDir(), "manifest-link")
				requireLink(t, manifest, alias)
				manifest = alias
			}
			err := Verify(root, manifest)
			if (err == nil) != (scenario == "clean") {
				t.Fatalf("unexpected result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("error leaked private bytes")
			}
		})
	}
}

func TestManifestCannotAuthorizePrivateNamesOrPrivateMaterial(t *testing.T) {
	for _, name := range []string{".env", "assets/.secret.svg", "assets/db.sqlite", "assets/key.pem", "assets/app.js.map", "server.go", "data/control.db", "assets/file.svg:stream"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			manifest := manifestFixture(t, root, map[string]string{"index.html": "<html/>"})
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var inventory Manifest
			if err := json.Unmarshal(data, &inventory); err != nil {
				t.Fatal(err)
			}
			inventory.Files = append(inventory.Files, AssetHash{Name: name, SHA256: strings.Repeat("0", 64)})
			data, err = json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Verify(root, manifest); err == nil {
				t.Fatal("manifest authorized forbidden file")
			}
		})
	}
	for _, body := range []string{"-----BEGIN PRIVATE KEY-----\nprivate-marker", "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-marker", "SQLite format 3\x00private-marker"} {
		root := t.TempDir()
		manifest := manifestFixture(t, root, map[string]string{"index.html": "<html/>", "assets/image.svg": body})
		if err := Verify(root, manifest); err == nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatalf("private material handling: %v", err)
		}
	}
	var scanner privateMaterialScanner
	_, _ = scanner.Write([]byte("prefix-----BEGIN OPENSSH "))
	if _, err := scanner.Write([]byte("PRIVATE KEY-----")); err == nil {
		t.Fatal("missed split private key header")
	}
}

func TestActualBuildOutputs(t *testing.T) {
	manifest := "../../frontend/public-assets-manifest.json"
	if _, err := os.Stat(manifest); os.IsNotExist(err) {
		t.Skip("run npm run build to verify actual build outputs")
	}
	if err := Verify("../../frontend/dist", manifest); err != nil {
		t.Fatal(err)
	}
}
