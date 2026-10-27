package publicassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
)

type Manifest struct {
	Version int         `json:"version"`
	Files   []AssetHash `json:"files"`
}

type AssetHash struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Verify checks the actual output tree against the inventory emitted by Vite.
// The inventory is stored outside the public tree and is not an HTTP endpoint.
func Verify(root, manifestPath string) error {
	manifestFile, err := Open(filepath.Dir(manifestPath), filepath.Base(manifestPath))
	if err != nil {
		return errors.New("cannot open public asset manifest")
	}
	defer manifestFile.Close()
	info, err := manifestFile.Stat()
	if err != nil || info.Size() > 1<<20 {
		return errors.New("public asset manifest exceeds its size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(manifestFile, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil || manifest.Version != 1 || len(manifest.Files) == 0 {
		return errors.New("invalid public asset manifest")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("invalid trailing public asset manifest data")
	}
	expected := make(map[string]string, len(manifest.Files))
	directories := map[string]bool{".": true}
	for _, asset := range manifest.Files {
		digest, err := hex.DecodeString(asset.SHA256)
		if ContentType(asset.Name) == "" || err != nil || len(digest) != sha256.Size {
			return errors.New("manifest contains a forbidden name or invalid hash")
		}
		if _, duplicate := expected[asset.Name]; duplicate {
			return errors.New("duplicate public asset manifest entry")
		}
		expected[asset.Name] = asset.SHA256
		for parent := path.Dir(asset.Name); parent != "."; parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	if _, ok := expected["index.html"]; !ok {
		return errors.New("public asset manifest is missing index.html")
	}
	if err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() && directories[name] {
			return nil
		}
		wanted, ok := expected[name]
		if !ok || !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected or non-regular public output: %q", name)
		}
		file, err := Open(root, name)
		if err != nil {
			return fmt.Errorf("public output cannot be opened without links: %q", name)
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(hash, &privateMaterialScanner{}), file); err != nil {
			return fmt.Errorf("unreadable or private material in public output: %q", name)
		}
		if hex.EncodeToString(hash.Sum(nil)) != wanted {
			return fmt.Errorf("public output hash mismatch: %q", name)
		}
		delete(expected, name)
		return nil
	}); err != nil {
		return err
	}
	if len(expected) != 0 {
		return errors.New("public output is missing manifest entries")
	}
	return nil
}

var privateKeyHeader = regexp.MustCompile(`-----BEGIN (RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)

// This detects recognizable material, not arbitrary application secrets. The
// exact build inventory and the prohibition on config/DB/key files also apply.
type privateMaterialScanner struct {
	prefix []byte
	tail   []byte
}

func (s *privateMaterialScanner) Write(data []byte) (int, error) {
	if len(s.prefix) < 16 {
		s.prefix = append(s.prefix, data[:min(len(data), 16-len(s.prefix))]...)
	}
	probe := append(s.tail, data...)
	if bytes.Equal(s.prefix, []byte("SQLite format 3\x00")) || privateKeyHeader.Match(probe) {
		return 0, errors.New("private file signature")
	}
	s.tail = append([]byte(nil), probe[max(0, len(probe)-128):]...)
	return len(data), nil
}
