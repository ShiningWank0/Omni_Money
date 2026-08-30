package validation

import (
	"strings"
	"testing"
)

func TestValidateTagNameCanonicalizesAndRejectsUnsafeValues(t *testing.T) {
	valid, err := ValidateTagName("  食費  ")
	if err != nil || valid != "食費" {
		t.Fatalf("canonical tag = %q, err = %v", valid, err)
	}
	for _, value := range []string{"", "   ", "a/b", "a\\b", "line\nbreak", "hidden\u200btext", strings.Repeat("a", MaxTagNameBytes+1)} {
		if _, err := ValidateTagName(value); err == nil {
			t.Errorf("ValidateTagName(%q) accepted unsafe value", value)
		}
	}
	if got, err := ValidateTagName(strings.Repeat("あ", MaxTagNameBytes/3)); err != nil || len([]byte(got)) > MaxTagNameBytes {
		t.Fatalf("boundary UTF-8 tag = %q, err = %v", got, err)
	}
}
