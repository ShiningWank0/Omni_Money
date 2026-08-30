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

// MaxTagLevel is the maximum supported tag depth, including the root level.
const MaxTagLevel = 3

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
		// U+2028/U+2029 are Unicode line separators. They are not always
		// classified as controls, but can split rendered/API text like a
		// newline and must not be accepted in a tag name.
		if r == '\u2028' || r == '\u2029' {
			return "", fmt.Errorf("タグ名に使用できない改行区切り文字が含まれています")
		}
		// Slash is the path separator used by CreateTagByPath. Backslash is
		// rejected too so a value has the same meaning across platforms.
		if r == '/' || r == '\\' {
			return "", fmt.Errorf("タグ名にパス区切り文字は使用できません")
		}
	}
	return name, nil
}

// ValidateTagLevel validates the persisted level independently of a parent.
func ValidateTagLevel(level int) error {
	if level < 1 || level > MaxTagLevel {
		return fmt.Errorf("タグ階層は1から%dの範囲で指定してください", MaxTagLevel)
	}
	return nil
}

// ValidateTagHierarchy enforces the invariant shared by every tag write and
// every import path: roots are level 1 and a child is exactly one level below
// its parent.
func ValidateTagHierarchy(level int, parentLevel *int) error {
	if err := ValidateTagLevel(level); err != nil {
		return err
	}
	if parentLevel == nil {
		if level != 1 {
			return fmt.Errorf("親のないタグはlevel 1である必要があります")
		}
		return nil
	}
	if err := ValidateTagLevel(*parentLevel); err != nil {
		return fmt.Errorf("親タグの階層が不正です: %w", err)
	}
	if *parentLevel+1 != level {
		return fmt.Errorf("タグの階層は親のlevel+1である必要があります")
	}
	return nil
}
