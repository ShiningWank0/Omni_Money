//go:build windows

package desktopaccount

import "golang.org/x/sys/windows"

func renameNoReplace(source, target string) error {
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourceName, targetName, windows.MOVEFILE_WRITE_THROUGH)
}
