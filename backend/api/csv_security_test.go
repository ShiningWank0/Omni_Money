package api

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeStrictCSVImportJSONRejectsDuplicateUnknownAndTrailingInput(t *testing.T) {
	for _, input := range []string{
		`{"content":"a","content":"b"}`,
		`{"content":"a","unexpected":true}`,
		`{"content":"a"}{"content":"b"}`,
		`["a"]`,
		`{"content":null}`,
	} {
		if _, err := decodeStrictCSVImportJSON(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted malformed CSV JSON: %s", input)
		}
	}
	malformedUTF8 := []byte{'{', '"', 'c', 'o', 'n', 't', 'e', 'n', 't', '"', ':', '"', 0xff, '"', '}'}
	if _, err := decodeStrictCSVImportJSON(bytes.NewReader(malformedUTF8)); err == nil {
		t.Fatal("accepted malformed UTF-8 CSV JSON")
	}
	body, err := decodeStrictCSVImportJSON(strings.NewReader(`{"content":"a","mode":"replace"}`))
	if err != nil || body.Content != "a" || body.Mode != "replace" {
		t.Fatalf("valid CSV JSON = %#v, err = %v", body, err)
	}
}
