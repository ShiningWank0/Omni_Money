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
	for _, value := range []string{"", "   ", "a/b", "a\\b", "line\nbreak", "hidden\u200btext", "line\u2028separator", "line\u2029separator", strings.Repeat("a", MaxTagNameBytes+1)} {
		if _, err := ValidateTagName(value); err == nil {
			t.Errorf("ValidateTagName(%q) accepted unsafe value", value)
		}
	}
	if got, err := ValidateTagName(strings.Repeat("あ", MaxTagNameBytes/3)); err != nil || len([]byte(got)) > MaxTagNameBytes {
		t.Fatalf("boundary UTF-8 tag = %q, err = %v", got, err)
	}
}

func TestValidateTagHierarchy(t *testing.T) {
	validParent := 1
	for _, tt := range []struct {
		name        string
		level       int
		parentLevel *int
		wantErr     bool
	}{
		{name: "root", level: 1},
		{name: "child", level: 2, parentLevel: &validParent},
		{name: "orphan child", level: 2, wantErr: true},
		{name: "bad level", level: 4, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateTagHierarchy(tt.level, tt.parentLevel); (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTagHierarchy(%d, %v) = %v, wantErr=%v", tt.level, tt.parentLevel, err, tt.wantErr)
			}
		})
	}
	badParent := 3
	if err := ValidateTagHierarchy(3, &badParent); err == nil {
		t.Fatal("level 3 child of level 3 was accepted")
	}
}
