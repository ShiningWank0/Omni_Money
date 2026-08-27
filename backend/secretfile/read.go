// Package secretfile securely opens bounded configuration and secret files.
package secretfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type policy uint8

const confidential policy = iota

// ReadConfidential reads a private regular file. Host files must be owner-only;
// read-only Docker secrets directly below /run/secrets may be 0444.
func ReadConfidential(path string, maxBytes int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("secret file path is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("secret file size limit must be positive")
	}

	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat secret file: %w", err)
	}
	if err := validateFileInfo(path, before, maxBytes); err != nil {
		return nil, err
	}

	handle, err := secureOpen(path, confidential)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer handle.Close()

	after, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened secret file: %w", err)
	}
	if !os.SameFile(before, after) {
		return nil, errors.New("secret file changed while being opened")
	}
	if err := validateFileInfo(path, after, maxBytes); err != nil {
		return nil, err
	}

	content, err := io.ReadAll(io.LimitReader(handle, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	if len(content) == 0 || int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("secret file size must be between 1 and %d bytes", maxBytes)
	}
	return content, nil
}

func validateFileInfo(path string, info os.FileInfo, maxBytes int64) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("secret file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("secret file must be a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return fmt.Errorf("secret file size must be between 1 and %d bytes", maxBytes)
	}

	permissions := info.Mode().Perm()
	if err := validateConfidentialPermissions(path, permissions); err != nil {
		return err
	}
	return validatePlatformConfidential(path)
}

func validateConfidentialPermissions(path string, permissions os.FileMode) error {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if filepath.ToSlash(filepath.Dir(cleaned)) == "/run/secrets" {
		if permissions&0o333 != 0 {
			return fmt.Errorf("Docker secret permissions are unsafe: %04o", permissions)
		}
		return nil
	}
	if permissions&0o177 != 0 {
		return fmt.Errorf("host secret file must be owner-only: %04o", permissions)
	}
	return nil
}
