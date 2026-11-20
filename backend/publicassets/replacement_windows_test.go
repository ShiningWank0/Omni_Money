//go:build windows

package publicassets

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func retryableAssetRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func TestWindowsAssetRenameRetriesOnlyKnownHandleConflicts(t *testing.T) {
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		if !retryableAssetRenameError(&os.LinkError{Op: "rename", Old: "next", New: "current.svg", Err: code}) {
			t.Errorf("handle conflict %v was not retryable", code)
		}
	}
	for _, err := range []error{nil, windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND, windows.ERROR_INVALID_NAME, os.ErrInvalid} {
		if retryableAssetRenameError(err) {
			t.Errorf("unexpected retryable error: %v", err)
		}
	}
}
