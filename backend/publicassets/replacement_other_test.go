//go:build !windows

package publicassets

import (
	"os"
	"testing"
)

func retryableAssetRenameError(error) bool { return false }

func TestAssetRenameOtherPlatformsDoNotRetryErrors(t *testing.T) {
	if retryableAssetRenameError(os.ErrPermission) {
		t.Fatal("non-Windows rename errors must remain immediate failures")
	}
}
