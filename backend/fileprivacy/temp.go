package fileprivacy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

// PrivateTempFile is a temporary file in a directory whose lifetime is owned
// by the caller. The file is created with CreateExclusive, so Windows applies
// its protected DACL in the CreateFile call rather than after content exists.
type PrivateTempFile struct {
	File *os.File
	Root *os.Root
	Dir  string
	Path string
}

// CreatePrivateTempFile creates a random, exclusive file in a private temp
// directory and returns the pinned root used for identity checks.
func CreatePrivateTempFile(prefix string) (*PrivateTempFile, error) {
	dir, root, err := CreatePrivateTempDir(prefix)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = root.Close()
		_ = os.RemoveAll(dir)
	}
	for attempt := 0; attempt < 16; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			cleanup()
			return nil, fmt.Errorf("一時ファイル名の生成に失敗しました: %w", err)
		}
		name := hex.EncodeToString(random[:]) + ".tmp"
		file, err := CreateExclusive(root, dir, name)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			cleanup()
			return nil, err
		}
		if err := Harden(file); err != nil {
			_ = file.Close()
			cleanup()
			return nil, err
		}
		return &PrivateTempFile{File: file, Root: root, Dir: dir, Path: dir + string(os.PathSeparator) + name}, nil
	}
	cleanup()
	return nil, fmt.Errorf("一時ファイル名の生成に失敗しました")
}

// Cleanup closes and removes the file and its private directory. It is safe
// to call more than once; callers should still report close/remove failures
// when they are security-relevant.
func (f *PrivateTempFile) Cleanup() error {
	if f == nil {
		return nil
	}
	var first error
	if f.File != nil {
		if err := f.File.Close(); err != nil && first == nil {
			first = err
		}
		f.File = nil
	}
	if f.Root != nil {
		if err := f.Root.Close(); err != nil && first == nil {
			first = err
		}
		f.Root = nil
	}
	if f.Dir != "" {
		if err := os.RemoveAll(f.Dir); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
		f.Dir = ""
	}
	return first
}
