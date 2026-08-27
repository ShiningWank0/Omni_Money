package aicredentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"omni_money/backend/secretfile"
)

const MaxFileSize int64 = 1 << 20

// LoadFile securely reads and validates a credential file. Group/other read
// bits are intentionally permitted for Docker secrets, while write and execute
// bits are rejected.
func LoadFile(path string) (*File, error) {
	if path == "" {
		return nil, errors.New("credential file path is required")
	}

	content, err := secretfile.ReadIntegrityProtected(path, MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	if err := rejectDuplicateJSONFields(content); err != nil {
		return nil, err
	}

	var document File
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode credential file: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := document.Validate(); err != nil {
		return nil, fmt.Errorf("validate credential file: %w", err)
	}
	return &document, nil
}

func validateFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("credential file must be a regular file")
	}
	if info.Size() > MaxFileSize {
		return fmt.Errorf("credential file exceeds %d bytes", MaxFileSize)
	}
	if hasInsecurePermissions(info.Mode().Perm()) {
		return fmt.Errorf("credential file permissions %04o allow group/other write or execute", info.Mode().Perm())
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("credential file contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing credential data: %w", err)
	}
	return nil
}

func rejectDuplicateJSONFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("validate credential JSON structure: %w", err)
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

// WriteFileAtomic validates and atomically replaces path with a mode-0600 JSON
// document. It never writes raw bearer tokens because File contains hashes only.
func WriteFileAtomic(path string, document *File) error {
	if path == "" {
		return errors.New("credential file path is required")
	}
	if document == nil {
		return errors.New("credential file is nil")
	}

	copyDocument := cloneFile(document)
	if err := copyDocument.Validate(); err != nil {
		return fmt.Errorf("validate credential file: %w", err)
	}
	content, err := json.MarshalIndent(copyDocument, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential file: %w", err)
	}
	content = append(content, '\n')
	if int64(len(content)) > MaxFileSize {
		return fmt.Errorf("credential file exceeds %d bytes", MaxFileSize)
	}

	if existing, err := os.Lstat(path); err == nil {
		if err := validateFileInfo(existing); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat credential file: %w", err)
	}

	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary credential file: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary credential file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary credential file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary credential file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	committed = true

	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

func cloneFile(document *File) *File {
	cloned := &File{Version: document.Version, Credentials: make([]Credential, len(document.Credentials))}
	for i := range document.Credentials {
		cloned.Credentials[i] = document.Credentials[i].clone()
	}
	return cloned
}
