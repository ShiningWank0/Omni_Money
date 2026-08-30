package validation

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTagNameBytes bounds the persisted representation of one tag component.
// The limit is in bytes because SQLite stores the value as UTF-8 text and the
// API must enforce the same limit regardless of the caller (Wails or HTTP).
const MaxTagNameBytes = 255

// ValidateTagName trims user-facing whitespace and rejects values that cannot
// be represented safely in a tag path or UI. The returned value is the
// canonical value that must be persisted by every write path.
func ValidateTagName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("タグ名は必須です")
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("タグ名は有効なUTF-8で指定してください")
	}
	if len([]byte(name)) > MaxTagNameBytes {
		return "", fmt.Errorf("タグ名は%dバイト以内で指定してください", MaxTagNameBytes)
	}
	for _, r := range name {
		// Control/format characters include newlines, NUL, bidi controls and
		// zero-width characters that can make a displayed path misleading.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("タグ名に使用できない文字が含まれています")
		}
		// Slash is the path separator used by CreateTagByPath. Backslash is
		// rejected too so a value has the same meaning across platforms.
		if r == '/' || r == '\\' {
			return "", fmt.Errorf("タグ名にパス区切り文字は使用できません")
		}
	}
	return name, nil
}
