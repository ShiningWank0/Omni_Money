package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"omni_money/backend/models"
)

const (
	minAIIdempotencyKeyBytes = 16
	maxAIIdempotencyKeyBytes = 128
)

func aiIdempotencyKeyHash(header http.Header) ([sha256.Size]byte, error) {
	values := header.Values("Idempotency-Key")
	if len(values) != 1 {
		return [sha256.Size]byte{}, errors.New("Idempotency-Keyヘッダーを1つ指定してください")
	}
	key := values[0]
	if len(key) < minAIIdempotencyKeyBytes || len(key) > maxAIIdempotencyKeyBytes {
		return [sha256.Size]byte{}, errors.New("Idempotency-Keyは16から128文字で指定してください")
	}
	for i := 0; i < len(key); i++ {
		character := key[i]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return [sha256.Size]byte{}, errors.New("Idempotency-Keyに使用できない文字が含まれています")
	}
	return sha256.Sum256([]byte(key)), nil
}

// canonicalAITransactionDigest hashes the normalized semantic request rather
// than its JSON representation, so JSON whitespace and object field order do
// not affect idempotency. Length prefixes prevent concatenation ambiguity and
// avoid allocating a second copy of potentially large image data.
func canonicalAITransactionDigest(request models.TransactionRequest) [sha256.Size]byte {
	digest := sha256.New()
	writeCanonicalString(digest, "omni-money/ai-transaction/v1")
	writeCanonicalString(digest, request.Account)
	writeCanonicalString(digest, request.Date)
	writeCanonicalString(digest, request.Time)
	writeCanonicalString(digest, request.Item)
	writeCanonicalString(digest, request.Type)
	writeCanonicalInt64(digest, request.Amount)
	writeCanonicalString(digest, request.Memo)

	tags := append([]int64(nil), request.Tags...)
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })
	writeCanonicalUint64(digest, uint64(len(tags)))
	for _, tagID := range tags {
		writeCanonicalInt64(digest, tagID)
	}

	imageDigests := make([][sha256.Size]byte, 0, len(request.Images))
	for _, image := range request.Images {
		imageDigest := sha256.New()
		writeCanonicalString(imageDigest, "omni-money/ai-transaction-image/v1")
		filename := strings.TrimSpace(image.Filename)
		mimeType := strings.ToLower(strings.TrimSpace(image.MimeType))
		if mimeType == "" {
			switch strings.ToLower(filepath.Ext(filename)) {
			case ".jpg", ".jpeg":
				mimeType = "image/jpeg"
			case ".png":
				mimeType = "image/png"
			case ".gif":
				mimeType = "image/gif"
			case ".webp":
				mimeType = "image/webp"
			}
		}
		writeCanonicalString(imageDigest, filename)
		writeCanonicalString(imageDigest, mimeType)
		writeCanonicalString(imageDigest, strings.TrimSpace(image.Data))
		var encoded [sha256.Size]byte
		copy(encoded[:], imageDigest.Sum(nil))
		imageDigests = append(imageDigests, encoded)
	}
	sort.Slice(imageDigests, func(i, j int) bool {
		return bytes.Compare(imageDigests[i][:], imageDigests[j][:]) < 0
	})
	writeCanonicalUint64(digest, uint64(len(imageDigests)))
	for _, imageDigest := range imageDigests {
		_, _ = digest.Write(imageDigest[:])
	}

	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeCanonicalString(destination hash.Hash, value string) {
	writeCanonicalUint64(destination, uint64(len(value)))
	_, _ = destination.Write([]byte(value))
}

func writeCanonicalInt64(destination hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = destination.Write(encoded[:])
}

func writeCanonicalUint64(destination hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = destination.Write(encoded[:])
}
