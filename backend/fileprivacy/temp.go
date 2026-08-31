package fileprivacy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
)

// PrivateTempFile is a temporary file in a directory whose lifetime is owned
// by the caller. The file is created with CreateExclusive, so Windows applies
// its protected DACL in the CreateFile call rather than after content exists.
type PrivateTempFile struct {
	File    *os.File
	Root    *os.Root
	Dir     string
	Path    string
	Name    string
	info    os.FileInfo
	dirInfo os.FileInfo
}

// removeCreatedPrivateDir is used only while unwinding creation of a newly
// made directory. Compare the original directory identity and use a
// non-recursive remove so a rename/replacement or unexpected child cannot turn
// error cleanup into deletion of unrelated data.
func removeCreatedPrivateDir(path string, created os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if created == nil || !os.SameFile(created, current) {
		return fmt.Errorf("private temp directory identity changed")
	}
	return os.Remove(path)
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
		_ = os.Remove(dir)
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
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			_ = root.Remove(name)
			_ = root.Close()
			_ = os.Remove(dir)
			return nil, statErr
		}
		dirInfo, statErr := os.Stat(dir)
		if statErr != nil {
			_ = file.Close()
			_ = root.Remove(name)
			_ = root.Close()
			_ = os.Remove(dir)
			return nil, statErr
		}
		return &PrivateTempFile{File: file, Root: root, Dir: dir, Path: dir + string(os.PathSeparator) + name, Name: name, info: info, dirInfo: dirInfo}, nil
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
		// Remove only the file created by this object through the retained root.
		// Never recursively remove the directory: a rename/replacement or an
		// unexpected extra entry must fail closed and remain for inspection.
		if f.Name != "" {
			current, err := f.Root.Lstat(f.Name)
			if err == nil {
				if f.info != nil && !os.SameFile(f.info, current) {
					if first == nil {
						first = fmt.Errorf("private temp file identity changed")
					}
				} else if err := f.Root.Remove(f.Name); err != nil && !os.IsNotExist(err) && first == nil {
					first = err
				}
			} else if !os.IsNotExist(err) && first == nil {
				first = err
			}
		}
		if entries, err := fs.ReadDir(f.Root.FS(), "."); err != nil {
			if first == nil {
				first = err
			}
		} else if len(entries) != 0 && first == nil {
			first = fmt.Errorf("private temp directory is not empty")
		}
		if err := f.Root.Close(); err != nil && first == nil {
			first = err
		}
		f.Root = nil
	}
	if f.Dir != "" {
		currentDir, statErr := os.Stat(f.Dir)
		if statErr != nil {
			if !os.IsNotExist(statErr) && first == nil {
				first = statErr
			}
		} else if f.dirInfo != nil && !os.SameFile(f.dirInfo, currentDir) {
			if first == nil {
				first = fmt.Errorf("private temp directory identity changed")
			}
		} else if err := os.Remove(f.Dir); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		} else if first == nil {
			f.Dir = ""
		}
	}
	f.Name = ""
	f.info = nil
	f.dirInfo = nil
	return first
}
