//go:build windows

package database

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procReplaceFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")
var procMoveFileEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")

const replaceFileWriteThrough = 0x1

func openSnapshotFile(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(
		handle, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("snapshot is a directory or reparse point")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap Windows snapshot handle")
	}
	if err := validateSnapshotHandle(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateSnapshotHandle(file *os.File) error {
	if file == nil {
		return errors.New("nil snapshot handle")
	}
	var byHandle windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &byHandle); err != nil {
		return err
	}
	if byHandle.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("snapshot handle is a directory or reparse point")
	}
	if byHandle.NumberOfLinks != 1 {
		return errors.New("snapshot must have exactly one hard link")
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return errors.New("snapshot owner is not the current Windows account")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("snapshot DACL is inheritable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 2 {
		return errors.New("snapshot DACL must contain only the owner and LocalSystem")
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	want := map[string]bool{user.User.Sid.String(): false, system.String(): false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return errors.New("snapshot DACL contains an unexpected ACE")
		}
		// SDDL's FA alias is FILE_ALL_ACCESS (0x1f01ff), not
		// STANDARD_RIGHTS_REQUIRED|SYNCHRONIZE|SPECIFIC_RIGHTS_ALL
		// (0x1fffff).  The latter accidentally grants reserved bits and is not
		// the same ACL that fileprivacy.Harden installs.
		const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
		if ace.Mask != fileAllAccess {
			return errors.New("snapshot DACL does not grant the exact private access mask")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			return errors.New("snapshot DACL grants an unexpected principal")
		}
		want[sid.String()] = true
	}
	for _, present := range want {
		if !present {
			return errors.New("snapshot DACL is missing a required principal")
		}
	}
	return nil
}

func validSnapshotFile(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	// Windows does not project its DACL into Unix mode bits. In particular,
	// normal owner+SYSTEM files often report 0666, so requiring 0600 here
	// rejects every legitimate encrypted snapshot. The handle opened with
	// FILE_FLAG_OPEN_REPARSE_POINT is checked separately; the caller also binds
	// the descriptor identity and single-link metadata before copying.
	return true
}

func snapshotModeAllowed(info os.FileInfo, _ bool) bool {
	// Unix permission projections are not authoritative on Windows. The
	// protected owner+SYSTEM DACL is checked on the opened handle instead.
	return info != nil
}

func snapshotDirectoryModeAllowed(info os.FileInfo) bool {
	// Windows ACLs are authoritative; os.FileMode permission bits are only a
	// compatibility projection and do not represent the protected owner+SYSTEM
	// DACL used for private snapshot directories.
	return info != nil
}

func syncDirectory(path string) error { return nil }

// ReplaceFileW replaces target with replacement atomically and asks Windows to
// retain the previous target at backup. This is the Windows equivalent of the
// POSIX rename replacement used by the restore transaction.
func replaceDatabaseFile(replacement, target, backup string) error {
	// ReplaceFileW creates the backup itself. Remove the pre-copy made by the
	// portable transaction first; the live target remains untouched if this
	// preparation or the replacement fails.
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	backupName, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	result, _, callErr := procReplaceFile.Call(
		uintptr(unsafe.Pointer(targetName)),
		uintptr(unsafe.Pointer(replacementName)),
		uintptr(unsafe.Pointer(backupName)),
		uintptr(replaceFileWriteThrough), 0, 0,
	)
	if result == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}

func installRecoveryFile(replacement, target, _ string) error {
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const moveFileReplaceExisting = 0x1
	const moveFileWriteThrough = 0x8
	result, _, callErr := procMoveFileEx.Call(
		uintptr(unsafe.Pointer(replacementName)),
		uintptr(unsafe.Pointer(targetName)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if result == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}

func replaceManifestFile(replacement, target string) error {
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const moveFileReplaceExisting = 0x1
	const moveFileWriteThrough = 0x8
	result, _, callErr := procMoveFileEx.Call(uintptr(unsafe.Pointer(replacementName)), uintptr(unsafe.Pointer(targetName)), uintptr(moveFileReplaceExisting|moveFileWriteThrough))
	if result == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}

// publishSnapshotFile uses the write-through Windows move primitive rather
// than os.Rename. The caller checks that target does not exist, and a
// concurrent collision fails closed rather than replacing another snapshot.
func publishSnapshotFile(replacement, target string) error {
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const moveFileWriteThrough = 0x8
	result, _, callErr := procMoveFileEx.Call(
		uintptr(unsafe.Pointer(replacementName)),
		uintptr(unsafe.Pointer(targetName)),
		uintptr(moveFileWriteThrough),
	)
	if result == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}

// publishSnapshotPruneManifestNoReplace uses the same atomic, write-through
// move as snapshot publication. Omitting MOVEFILE_REPLACE_EXISTING preserves
// an unresolved fixed-name journal on collision.
func publishSnapshotPruneManifestNoReplace(replacement, target string) error {
	replacementName, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const moveFileWriteThrough = 0x8
	result, _, callErr := procMoveFileEx.Call(
		uintptr(unsafe.Pointer(replacementName)),
		uintptr(unsafe.Pointer(targetName)),
		uintptr(moveFileWriteThrough),
	)
	if result == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}
