//go:build windows

package securedb

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createPrivateMigrationDirectory applies a protected DACL in the same
// CreateDirectory call, before SQLite can create plaintext WAL/journal files.
func createPrivateMigrationDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current Windows account: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)",
	)
	if err != nil {
		return fmt.Errorf("create private Windows directory DACL: %w", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode private Windows directory path: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(pathPointer, attributes); err != nil {
		return fmt.Errorf("create private Windows migration directory: %w", err)
	}
	return nil
}
