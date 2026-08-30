package validation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ledger text limits are shared by the Desktop, Web, AI, and CSV paths.  They
// are byte limits because SQLite stores UTF-8 and because request/file limits
// must not depend on the number of Unicode code points.
const (
	MaxAccountBytes     = 256
	MaxItemBytes        = 512
	MaxMemoBytes        = 4096
	MaxSettingItemBytes = 255
	MaxSettingItems     = 4096
)

// ValidateLedgerText validates text that is persisted in a ledger row.  It
// intentionally never trims or otherwise canonicalizes the value: export and
// import must preserve the exact value accepted by normal writes.  Newlines,
// tabs, and carriage returns are retained for backwards-compatible memos;
// all other C0/C1 controls, NUL, Unicode format/bidi controls, and line
// separators are rejected.
func ValidateLedgerText(label, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%sは必須です", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%sはUTF-8で指定してください", label)
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("%sは%dバイト以内にしてください", label, maxBytes)
	}
	for _, r := range value {
		if r == '\x00' || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("%sに使用できない文字が含まれています", label)
		}
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return fmt.Errorf("%sに制御文字を含めることはできません", label)
		}
	}
	return nil
}

// ValidateArchivedLedgerText is the compatibility policy for rows that were
// already persisted before the strict ledger text policy was introduced. It
// preserves bytes exactly (including historical control/format characters) and
// only rejects invalid UTF-8, NUL, and values too large for the archive field.
// New writes and ordinary v3 rows must use ValidateLedgerText instead. Export
// marks such legacy rows explicitly, making this trust boundary auditable.
func ValidateArchivedLedgerText(label, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%sは必須です", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%sはUTF-8で指定してください", label)
	}
	if strings.IndexByte(value, '\x00') >= 0 {
		return fmt.Errorf("%sにNUL文字を含めることはできません", label)
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("%sは%dバイト以内にしてください", label, maxBytes)
	}
	return nil
}

// ValidateLedgerSettingItems applies the same setting semantics to normal
// writes and imports. Values are not trimmed, so a round-trip cannot silently
// change a setting that was accepted by the API.
func ValidateLedgerSettingItems(items []string) error {
	if len(items) > MaxSettingItems {
		return fmt.Errorf("設定項目は%d件以内で指定してください", MaxSettingItems)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := ValidateLedgerText("設定値の項目", item, MaxSettingItemBytes, true); err != nil {
			return err
		}
		if _, ok := seen[item]; ok {
			return fmt.Errorf("設定値の項目が重複しています")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func MarshalLedgerSettingItems(items []string) (string, error) {
	if err := ValidateLedgerSettingItems(items); err != nil {
		return "", err
	}
	// JSON null cannot be parsed back into the required string-array shape.
	// Treat a nil Go slice as the explicitly empty setting so Save(nil) has a
	// stable, lossless round-trip with Get and CSV v3.
	if items == nil {
		items = []string{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("JSONシリアライズエラー: %w", err)
	}
	return string(b), nil
}

func ParseLedgerSettingItems(value string) ([]string, error) {
	if !utf8.ValidString(value) || value == "" {
		return nil, fmt.Errorf("設定値は文字列配列JSONで指定してください")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	var items []string
	if err := decoder.Decode(&items); err != nil || items == nil {
		return nil, fmt.Errorf("設定値は文字列配列JSONで指定してください")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("設定値JSONに余分なデータがあります")
	}
	if err := ValidateLedgerSettingItems(items); err != nil {
		return nil, err
	}
	return items, nil
}
