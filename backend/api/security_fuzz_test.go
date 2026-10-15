package api

import (
	"encoding/json"
	"testing"
)

func FuzzStrictServerJSONKeys(f *testing.F) {
	for _, seed := range []string{`{}`, `{"key":1,"key":2}`, `{"key":1,"\u006bey":2}`, `[{"a":[{"b":1,"b":2}]}]`, `{"a":1} {}`, `[[[[`, `null`, `1e9999`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 8*1024 {
			t.Skip()
		}
		if rejectDuplicateServerJSONKeys(input) == nil && !json.Valid(input) {
			t.Fatal("strict scanner accepted malformed or trailing JSON")
		}
		// Independently generated valid strings cover escaping, invalid input UTF-8
		// replacement and Unicode keys without depending on the scanner's parser.
		key, err := json.Marshal(string(input) + "a")
		if err != nil {
			t.Fatal(err)
		}
		unique := []byte(`{"outer":[{` + string(key) + `:0},{` + string(key) + `:1}]}`)
		if rejectDuplicateServerJSONKeys(unique) != nil {
			t.Fatal("equal keys in different objects were rejected")
		}
		// Escape the final ASCII suffix differently to verify comparison happens
		// after JSON key decoding, including Unicode escape aliases.
		aliasedKey := string(key[:len(key)-2]) + `\u0061"`
		duplicate := `{` + string(key) + `:0,` + aliasedKey + `:1}`
		for _, body := range []string{duplicate, `{"outer":[` + duplicate + `]}`, string(unique) + ` null`} {
			if rejectDuplicateServerJSONKeys([]byte(body)) == nil {
				t.Fatal("duplicate key or trailing JSON bypassed strict validation")
			}
		}
	})
}
