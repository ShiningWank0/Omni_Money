//go:build windows

package desktopaccount

import (
	"os"

	"golang.org/x/sys/windows"
)

func hasInsecurePermissions(_ os.FileMode) bool { return false }

func openRegularNoFollow(path string) (*os.File, error) { return os.Open(path) }

func replaceFileAtomic(source, target string) error {
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourceName, targetName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncDirectory(_ string) error { return nil }
