// Package securedb provides fail-closed SQLCipher database connections.
package securedb

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"omni_money/backend/secretfile"
)

const (
	RawKeySize      = 32
	maxKeyFileBytes = 256
)

type RawKey [RawKeySize]byte

func NewRawKey(raw []byte) (RawKey, error) {
	var key RawKey
	if len(raw) != RawKeySize {
		return key, fmt.Errorf("database encryption key must be exactly %d bytes", RawKeySize)
	}
	copy(key[:], raw)
	return key, nil
}

func ParseRawKey(encoded string) (RawKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return RawKey{}, errors.New("database encryption key is empty")
	}
	decoders := []func(string) ([]byte, error){
		hex.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
	}
	for _, decode := range decoders {
		raw, err := decode(encoded)
		if err != nil {
			continue
		}
		key, err := NewRawKey(raw)
		clear(raw)
		if err == nil {
			return key, nil
		}
	}
	return RawKey{}, fmt.Errorf("database encryption key must decode to exactly %d bytes", RawKeySize)
}

func ReadRawKeyFile(path string) (RawKey, error) {
	content, err := secretfile.ReadConfidential(strings.TrimSpace(path), maxKeyFileBytes)
	if err != nil {
		return RawKey{}, fmt.Errorf("read database encryption key: %w", err)
	}
	defer clear(content)
	return ParseRawKey(string(content))
}

func (key *RawKey) Destroy() {
	if key != nil {
		clear(key[:])
	}
}
