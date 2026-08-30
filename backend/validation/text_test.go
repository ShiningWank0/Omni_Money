package validation

import (
	"strings"
	"testing"
)

func TestValidateLedgerTextRejectsUnsafeTextWithoutCanonicalizing(t *testing.T) {
	if err := ValidateLedgerText("account", "  cash  ", MaxAccountBytes, true); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"cash\x00", "hidden\u200btext", "bidi\u202etext", "line\u2028separator", "line\u0001"} {
		if err := ValidateLedgerText("account", value, MaxAccountBytes, true); err == nil {
			t.Errorf("unsafe value %q was accepted", value)
		}
	}
	if err := ValidateLedgerText("account", string([]byte{0xff}), MaxAccountBytes, true); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestLedgerSettingItemsHaveStableNilAndDuplicateSemantics(t *testing.T) {
	encoded, err := MarshalLedgerSettingItems(nil)
	if err != nil || encoded != "[]" {
		t.Fatalf("nil setting = %q, err = %v", encoded, err)
	}
	items, err := ParseLedgerSettingItems(encoded)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("parsed nil setting = %#v, err = %v", items, err)
	}
	if _, err := MarshalLedgerSettingItems([]string{"card", "card"}); err == nil {
		t.Fatal("duplicate setting items were accepted")
	}
	if _, err := MarshalLedgerSettingItems([]string{""}); err == nil {
		t.Fatal("empty setting item was accepted")
	}
	if _, err := MarshalLedgerSettingItems([]string{strings.Repeat("x", MaxSettingItemBytes+1)}); err == nil {
		t.Fatal("oversized setting item was accepted")
	}
}
