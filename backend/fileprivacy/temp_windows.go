//go:build windows

package fileprivacy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CreatePrivateTempDir protects the directory before any archive bytes are
// created. Individual files are additionally protected atomically by
// CreateExclusive because Windows mode bits do not express privacy.
func CreatePrivateTempDir(prefix string) (string, *os.Root, error) {
	descriptor, err := privateSecurityDescriptor()
	if err != nil {
		return "", nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	parent := os.TempDir()
	for attempt := 0; attempt < 16; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, fmt.Errorf("一時ディレクトリ名の生成に失敗しました: %w", err)
		}
		dir := filepath.Join(parent, prefix+hex.EncodeToString(random[:]))
		pathPointer, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return "", nil, err
		}
		if err := windows.CreateDirectory(pathPointer, attributes); err != nil {
			if err == windows.ERROR_ALREADY_EXISTS {
				continue
			}
			return "", nil, err
		}
		created, statErr := os.Lstat(dir)
		if statErr != nil {
			_ = os.Remove(dir)
			return "", nil, statErr
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			_ = removeCreatedPrivateDir(dir, created)
			return "", nil, err
		}
		return dir, root, nil
	}
	return "", nil, fmt.Errorf("一時ディレクトリ名の生成に失敗しました")
}
