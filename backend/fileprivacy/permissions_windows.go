//go:build windows

// Package fileprivacy applies OS-specific owner-only permissions to files.
package fileprivacy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ALL_ACCESS is the standard file object access mask. x/sys/windows does
// not expose this aggregate constant on all supported versions.
const fileAllAccess uint32 = 0x1F01FF

func privateSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows account: %w", err)
	}
	sid := user.User.Sid.String()
	// LocalSystem's current SID is already SY. Avoid emitting duplicate ACEs;
	// the DACL still grants exactly the same principal set.
	sddl := "O:" + sid + "D:P(A;;FA;;;" + sid + ")"
	if sid != "S-1-5-18" {
		sddl += "(A;;FA;;;SY)"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("create private Windows DACL: %w", err)
	}
	return descriptor, nil
}

func privateDirectorySecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows account: %w", err)
	}
	sid := user.User.Sid.String()
	sddl := "O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")"
	if sid != "S-1-5-18" {
		sddl += "(A;OICI;FA;;;SY)"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("create private Windows directory DACL: %w", err)
	}
	return descriptor, nil
}

// CreateExclusive creates the file with its protected DACL in the same
// CreateFile call. It then proves that the created handle names the file under
// the directory root pinned before sensitive content was generated.
func CreateExclusive(root *os.Root, directory, name string) (*os.File, error) {
	descriptor, err := privateSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private Windows path: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap private Windows file handle")
	}

	createdInfo, createdErr := file.Stat()
	rootInfo, rootErr := root.Lstat(name)
	validationErr := ValidatePrivateFile(file)
	if createdErr != nil || rootErr != nil || !os.SameFile(createdInfo, rootInfo) || validationErr != nil {
		cleanupErr := deleteOpenWindowsFile(handle)
		closeErr := file.Close()
		return nil, errors.Join(
			errors.New("created Windows file is outside the pinned destination"),
			createdErr,
			rootErr,
			validationErr,
			cleanupErr,
			closeErr,
		)
	}
	return file, nil
}

func deleteOpenWindowsFile(handle windows.Handle) error {
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(
		handle,
		windows.FileDispositionInfo,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
	)
}

// Harden replaces inherited permissions before any sensitive bytes are
// written. Windows ignores Unix mode bits, so explicitly allow only the
// current account and LocalSystem and protect the DACL from inheritance.
func Harden(file *os.File) error {
	if file == nil {
		return errors.New("private file handle is nil")
	}
	originalInfo, err := file.Stat()
	if err != nil {
		return err
	}
	secure, err := openPrivateFileForSecurity(file.Name(), windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer secure.Close()
	secureInfo, err := secure.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(originalInfo, secureInfo) {
		return errors.New("private file changed before hardening")
	}
	descriptor, err := privateSecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private Windows DACL: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.Join(errors.New("read private Windows owner"), err)
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(secure.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply private Windows DACL: %w", err)
	}
	post, err := openPrivateFileForSecurity(file.Name(), windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer post.Close()
	postInfo, err := post.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(originalInfo, postInfo) {
		return errors.New("private file changed while hardening")
	}
	return ValidatePrivateFile(post)
}

func openPrivateFileForSecurity(path string, access uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap private Windows security handle")
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		_ = file.Close()
		return nil, err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = file.Close()
		return nil, errors.New("private file is a directory or reparse point")
	}
	return file, nil
}

// IsPrivate proves the actual DACL on the open handle rather than trusting
// Windows' emulated FileInfo mode bits. Only the current user and LocalSystem
// may have full access, and inheritance must be disabled.
func IsPrivate(file *os.File, info os.FileInfo) bool {
	if file == nil || info == nil || !info.Mode().IsRegular() {
		return false
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return false
	}
	want := map[string]bool{current.User.Sid.String(): false}
	if current.User.Sid.String() != system.String() {
		want[system.String()] = false
	}
	if int(dacl.AceCount) != len(want) {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != windows.ACCESS_MASK(fileAllAccess) {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			return false
		}
		if want[sid.String()] {
			return false
		}
		want[sid.String()] = true
	}
	for _, present := range want {
		if !present {
			return false
		}
	}
	return true
}

// ValidatePrivateFile checks the descriptor itself. Windows mode bits are not
// authoritative; the protected owner+SYSTEM DACL and file identity are.
func ValidatePrivateFile(file *os.File) error {
	if file == nil {
		return errors.New("private file handle is nil")
	}
	if err := validatePrivateFileHandle(file); err != nil {
		return err
	}
	return nil
}

func validatePrivateFileHandle(file *os.File) error {
	handle := windows.Handle(file.Fd())
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		return err
	}
	if attributes.Attributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("private file is a directory or reparse point")
	}
	var byHandle windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &byHandle); err != nil {
		return err
	}
	if byHandle.NumberOfLinks != 1 {
		return errors.New("private file must have exactly one hard link")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return errors.New("private file owner is not the current Windows account")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private file DACL is inheritable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("private file DACL must contain only owner and LocalSystem")
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	want := map[string]bool{user.User.Sid.String(): false}
	if user.User.Sid.String() != system.String() {
		want[system.String()] = false
	}
	if int(dacl.AceCount) != len(want) {
		return errors.New("private file DACL must contain only owner and LocalSystem")
	}
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != fileAllAccess {
			return errors.New("private file DACL contains an unexpected ACE")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			return errors.New("private file DACL grants an unexpected principal")
		}
		want[sid.String()] = true
	}
	for _, present := range want {
		if !present {
			return errors.New("private file DACL is missing a required principal")
		}
	}
	return nil
}

func openPrivateDirectory(path string, access uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap private Windows directory handle")
	}
	var attributes struct {
		Attributes uint32
		ReparseTag uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes))); err != nil {
		_ = file.Close()
		return nil, err
	}
	if attributes.Attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attributes.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return nil, errors.New("private path is not a real Windows directory")
	}
	return file, nil
}

func HardenDirectory(path string) error {
	file, err := openPrivateDirectory(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	descriptor, err := privateDirectorySecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.Join(errors.New("read private Windows directory owner"), err)
	}
	if err := windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("apply private Windows directory DACL: %w", err)
	}
	post, err := openPrivateDirectory(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer post.Close()
	after, err := post.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return errors.New("private directory changed while hardening")
	}
	return validatePrivateDACL(post)
}

func ValidateDirectory(path string) error {
	file, err := openPrivateDirectory(path, windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer file.Close()
	return ValidatePrivateDirectory(file)
}

// ValidatePrivateDirectory validates the exact acquired directory handle,
// including its protected owner+SYSTEM DACL. Callers that pin a transaction
// root must not fall back to a pathname-only privacy check after acquisition.
func ValidatePrivateDirectory(file *os.File) error {
	if file == nil {
		return errors.New("nil private directory handle")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("private handle is not a directory")
	}
	return validatePrivateDACL(file)
}

func validatePrivateDACL(file *os.File) error {
	if file == nil {
		return errors.New("nil private handle")
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return errors.New("private directory owner is not the current Windows account")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private directory DACL is inheritable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("private directory DACL must contain only the owner and LocalSystem")
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	want := map[string]bool{user.User.Sid.String(): false}
	if user.User.Sid.String() != system.String() {
		want[system.String()] = false
	}
	if int(dacl.AceCount) != len(want) {
		return errors.New("private directory DACL must contain only the owner and LocalSystem")
	}
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || ace.Mask != fileAllAccess {
			return errors.New("private directory DACL contains an unexpected ACE")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if _, ok := want[sid.String()]; !ok {
			return errors.New("private directory DACL grants an unexpected principal")
		}
		want[sid.String()] = true
	}
	for _, present := range want {
		if !present {
			return errors.New("private directory DACL is missing a required principal")
		}
	}
	return nil
}
