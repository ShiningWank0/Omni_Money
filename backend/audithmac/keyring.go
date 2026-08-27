// Package audithmac provides rotation-aware, domain-separated pseudonymous
// references for AI audit events. Raw key material never leaves this package.
package audithmac

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"omni_money/backend/secretfile"
)

const (
	CurrentVersion = 1
	MaxFileSize    = int64(4096)
	MinKeyBytes    = 32
	MaxKeyBytes    = 64
	MinOverlap     = time.Second
	DefaultOverlap = 7 * 24 * time.Hour
	MaxOverlap     = 30 * 24 * time.Hour

	accountKeyDomain = "omni-money/audit/account-key/v1"
	keyIDDomain      = "omni-money/audit/key-id/v1"
)

type previousDocument struct {
	Key       string `json:"key"`
	EmitUntil string `json:"emit_until"`
}

type keyringDocument struct {
	Version    int               `json:"version"`
	CurrentKey string            `json:"current_key"`
	Previous   *previousDocument `json:"previous,omitempty"`
}

type keyMaterial struct {
	accountKey [sha256.Size]byte
	keyID      string
	emitUntil  time.Time
}

type snapshotData struct {
	current  keyMaterial
	previous *keyMaterial
}

// Reference is safe to place in an audit event. It contains no key material.
type Reference struct {
	KeyID      string
	HMACSHA256 string
}

// ReferenceSet contains the current reference and, only during a configured
// overlap, a previous-key reference that bridges the rotation boundary.
type ReferenceSet struct {
	Current  Reference
	Previous *Reference
}

// Snapshot is an immutable request-scoped view of a complete keyring.
type Snapshot struct{ data *snapshotData }

// AccountReferences pseudonymizes account exactly as supplied. Account names
// are deliberately not trimmed, case-folded, or Unicode-normalized because
// those transformations could merge distinct database accounts.
func (s Snapshot) AccountReferences(account string, at time.Time) ReferenceSet {
	if s.data == nil || account == "" {
		return ReferenceSet{}
	}
	result := ReferenceSet{Current: referenceFor(&s.data.current, account)}
	if previous := s.data.previous; previous != nil && at.Before(previous.emitUntil) {
		value := referenceFor(previous, account)
		result.Previous = &value
	}
	return result
}

func referenceFor(key *keyMaterial, account string) Reference {
	mac := hmac.New(sha256.New, key.accountKey[:])
	_, _ = mac.Write([]byte(account))
	return Reference{KeyID: key.keyID, HMACSHA256: hex.EncodeToString(mac.Sum(nil))}
}

// Store atomically publishes complete immutable keyring snapshots. A failed
// reload never displaces the last valid snapshot.
type Store struct {
	path     string
	reloadMu sync.Mutex
	current  atomic.Pointer[snapshotData]
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("audit HMAC keyring path is required")
	}
	store := &Store{path: path}
	if err := store.Reload(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Reload() error {
	if s == nil {
		return errors.New("audit HMAC store is nil")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	document, err := loadDocument(s.path)
	if err != nil {
		return err
	}
	snapshot, err := buildSnapshot(document, time.Now().UTC())
	if err != nil {
		return err
	}
	s.current.Store(snapshot)
	return nil
}

func (s *Store) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	return Snapshot{data: s.current.Load()}
}

func (s *Store) CurrentKeyID() string {
	snapshot := s.Snapshot()
	if snapshot.data == nil {
		return ""
	}
	return snapshot.data.current.keyID
}

func loadDocument(path string) (*keyringDocument, error) {
	content, err := secretfile.ReadConfidential(path, MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("read audit HMAC keyring: %w", err)
	}
	defer clear(content)
	return decodeDocument(content)
}

func decodeDocument(content []byte) (*keyringDocument, error) {
	if err := rejectDuplicateJSONFields(content); err != nil {
		return nil, err
	}
	var document keyringDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode audit HMAC keyring: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return &document, nil
}

func buildSnapshot(document *keyringDocument, now time.Time) (*snapshotData, error) {
	if document == nil {
		return nil, errors.New("audit HMAC keyring is nil")
	}
	if document.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported audit HMAC keyring version %d", document.Version)
	}
	currentRaw, err := decodeKey(document.CurrentKey)
	if err != nil {
		return nil, fmt.Errorf("current audit HMAC key is invalid: %w", err)
	}
	defer clear(currentRaw)

	result := &snapshotData{current: deriveKeyMaterial(currentRaw)}
	if document.Previous == nil {
		return result, nil
	}
	previousRaw, err := decodeKey(document.Previous.Key)
	if err != nil {
		return nil, fmt.Errorf("previous audit HMAC key is invalid: %w", err)
	}
	defer clear(previousRaw)
	if hmac.Equal(currentRaw, previousRaw) {
		return nil, errors.New("current and previous audit HMAC keys must differ")
	}
	emitUntil, err := time.Parse(time.RFC3339, document.Previous.EmitUntil)
	if err != nil || emitUntil.UTC().Format(time.RFC3339) != document.Previous.EmitUntil {
		return nil, errors.New("previous audit HMAC emit_until must be canonical UTC RFC3339")
	}
	if emitUntil.After(now.Add(MaxOverlap)) {
		return nil, fmt.Errorf("previous audit HMAC overlap exceeds %s", MaxOverlap)
	}
	previous := deriveKeyMaterial(previousRaw)
	previous.emitUntil = emitUntil
	result.previous = &previous
	return result, nil
}

func decodeKey(encoded string) ([]byte, error) {
	if encoded == "" || strings.TrimSpace(encoded) != encoded || strings.Contains(encoded, "=") {
		return nil, errors.New("key must use unpadded base64url without whitespace")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("key must use canonical unpadded base64url")
	}
	if len(decoded) < MinKeyBytes || len(decoded) > MaxKeyBytes {
		clear(decoded)
		return nil, fmt.Errorf("key must decode to between %d and %d bytes", MinKeyBytes, MaxKeyBytes)
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		clear(decoded)
		return nil, errors.New("key must use canonical unpadded base64url")
	}
	return decoded, nil
}

func deriveKeyMaterial(master []byte) keyMaterial {
	accountMAC := hmac.New(sha256.New, master)
	_, _ = accountMAC.Write([]byte(accountKeyDomain))
	accountKeyBytes := accountMAC.Sum(nil)
	var accountKey [sha256.Size]byte
	copy(accountKey[:], accountKeyBytes)
	clear(accountKeyBytes)

	idMAC := hmac.New(sha256.New, master)
	_, _ = idMAC.Write([]byte(keyIDDomain))
	idBytes := idMAC.Sum(nil)
	keyID := "ak1-" + hex.EncodeToString(idBytes[:16])
	clear(idBytes)
	return keyMaterial{accountKey: accountKey, keyID: keyID}
}

func rejectDuplicateJSONFields(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("validate audit HMAC JSON structure: %w", err)
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

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("audit HMAC keyring contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing audit HMAC data: %w", err)
	}
	return nil
}

// FileStatus contains only non-secret identifiers suitable for CLI output.
type FileStatus struct {
	CurrentKeyID    string
	PreviousKeyID   string
	PreviousExpires time.Time
}

func InitializeFile(path string, random io.Reader) (FileStatus, error) {
	if strings.TrimSpace(path) == "" {
		return FileStatus{}, errors.New("audit HMAC keyring path is required")
	}
	key, err := generateKey(random)
	if err != nil {
		return FileStatus{}, err
	}
	document := &keyringDocument{Version: CurrentVersion, CurrentKey: key}
	now := time.Now().UTC()
	if err := writeDocumentExclusive(path, document, now); err != nil {
		return FileStatus{}, err
	}
	return statusFor(document, now)
}

func RotateFile(path string, random io.Reader, now time.Time, overlap time.Duration) (FileStatus, error) {
	if strings.TrimSpace(path) == "" {
		return FileStatus{}, errors.New("audit HMAC keyring path is required")
	}
	if now.IsZero() {
		return FileStatus{}, errors.New("rotation time is required")
	}
	if overlap < MinOverlap || overlap > MaxOverlap {
		return FileStatus{}, fmt.Errorf("overlap must be between %s and %s", MinOverlap, MaxOverlap)
	}
	document, err := loadDocument(path)
	if err != nil {
		return FileStatus{}, err
	}
	if _, err := buildSnapshot(document, now.UTC()); err != nil {
		return FileStatus{}, err
	}
	if document.Previous != nil {
		expires, _ := time.Parse(time.RFC3339, document.Previous.EmitUntil)
		if now.UTC().Before(expires) {
			return FileStatus{}, errors.New("previous audit HMAC overlap is still active; retire it before rotating again")
		}
	}
	newKey, err := generateKey(random)
	if err != nil {
		return FileStatus{}, err
	}
	document.Previous = &previousDocument{
		Key:       document.CurrentKey,
		EmitUntil: now.UTC().Add(overlap).Truncate(time.Second).Format(time.RFC3339),
	}
	document.CurrentKey = newKey
	if err := writeDocumentAtomic(path, document, now.UTC()); err != nil {
		return FileStatus{}, err
	}
	return statusFor(document, now.UTC())
}

func RetirePreviousFile(path string, now time.Time) (FileStatus, error) {
	if strings.TrimSpace(path) == "" {
		return FileStatus{}, errors.New("audit HMAC keyring path is required")
	}
	if now.IsZero() {
		return FileStatus{}, errors.New("retirement time is required")
	}
	document, err := loadDocument(path)
	if err != nil {
		return FileStatus{}, err
	}
	if _, err := buildSnapshot(document, now.UTC()); err != nil {
		return FileStatus{}, err
	}
	document.Previous = nil
	if err := writeDocumentAtomic(path, document, now.UTC()); err != nil {
		return FileStatus{}, err
	}
	return statusFor(document, now.UTC())
}

func generateKey(random io.Reader) (string, error) {
	if random == nil {
		return "", errors.New("random source is required")
	}
	key := make([]byte, MinKeyBytes)
	defer clear(key)
	if _, err := io.ReadFull(random, key); err != nil {
		return "", fmt.Errorf("generate audit HMAC key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

func statusFor(document *keyringDocument, now time.Time) (FileStatus, error) {
	snapshot, err := buildSnapshot(document, now)
	if err != nil {
		return FileStatus{}, err
	}
	status := FileStatus{CurrentKeyID: snapshot.current.keyID}
	if snapshot.previous != nil {
		status.PreviousKeyID = snapshot.previous.keyID
		status.PreviousExpires = snapshot.previous.emitUntil
	}
	return status, nil
}

func writeDocumentAtomic(path string, document *keyringDocument, validationTime time.Time) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("audit HMAC keyring path is required")
	}
	content, err := encodeDocument(document, validationTime)
	if err != nil {
		return err
	}
	defer clear(content)
	if _, err := os.Lstat(path); err == nil {
		existingContent, err := secretfile.ReadConfidential(path, MaxFileSize)
		if err != nil {
			return fmt.Errorf("existing audit HMAC keyring is unsafe: %w", err)
		}
		clear(existingContent)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat audit HMAC keyring: %w", err)
	}

	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary audit HMAC keyring: %w", err)
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
		return fmt.Errorf("secure temporary audit HMAC keyring: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary audit HMAC keyring: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary audit HMAC keyring: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary audit HMAC keyring: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace audit HMAC keyring: %w", err)
	}
	committed = true
	return syncDirectory(directory)
}

func writeDocumentExclusive(path string, document *keyringDocument, validationTime time.Time) error {
	content, err := encodeDocument(document, validationTime)
	if err != nil {
		return err
	}
	defer clear(content)

	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create audit HMAC keyring without overwriting: %w", err)
	}
	committed := false
	defer func() {
		_ = handle.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if err := handle.Chmod(0o600); err != nil {
		return fmt.Errorf("secure audit HMAC keyring: %w", err)
	}
	if _, err := handle.Write(content); err != nil {
		return fmt.Errorf("write audit HMAC keyring: %w", err)
	}
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync audit HMAC keyring: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close audit HMAC keyring: %w", err)
	}
	committed = true
	return syncDirectory(filepath.Dir(path))
}

func encodeDocument(document *keyringDocument, validationTime time.Time) ([]byte, error) {
	if _, err := buildSnapshot(document, validationTime.UTC()); err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, errors.New("encode audit HMAC keyring")
	}
	content = append(content, '\n')
	if int64(len(content)) > MaxFileSize {
		clear(content)
		return nil, fmt.Errorf("audit HMAC keyring exceeds %d bytes", MaxFileSize)
	}
	return content, nil
}
