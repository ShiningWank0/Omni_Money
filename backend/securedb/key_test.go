package securedb

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseRawKey(t *testing.T) {
	raw := make([]byte, RawKeySize)
	for index := range raw {
		raw[index] = byte(index + 1)
	}
	for name, encoded := range map[string]string{
		"hex":        hex.EncodeToString(raw),
		"base64":     base64.StdEncoding.EncodeToString(raw),
		"raw-base64": base64.RawStdEncoding.EncodeToString(raw),
	} {
		t.Run(name, func(t *testing.T) {
			key, err := ParseRawKey("  " + encoded + "\n")
			if err != nil {
				t.Fatal(err)
			}
			if string(key[:]) != string(raw) {
				t.Fatal("decoded key differs")
			}
			key.Destroy()
			if strings.Trim(string(key[:]), "\x00") != "" {
				t.Fatal("Destroy did not clear key material")
			}
		})
	}
}

func TestParseRawKeyRejectsInvalidSizes(t *testing.T) {
	for _, encoded := range []string{"", "not-a-key", strings.Repeat("00", RawKeySize-1), strings.Repeat("00", RawKeySize+1)} {
		if _, err := ParseRawKey(encoded); err == nil {
			t.Fatalf("ParseRawKey(%q) unexpectedly succeeded", encoded)
		}
	}
}

func TestOpenerFormattingDoesNotExposeKey(t *testing.T) {
	key, err := ParseRawKey(strings.Repeat("a5", RawKeySize))
	if err != nil {
		t.Fatal(err)
	}
	opener := NewEncryptedOpener(key)
	formatted := opener.String() + " " + opener.GoString()
	if strings.Contains(formatted, strings.Repeat("a5", RawKeySize)) {
		t.Fatal("formatted opener exposed database key")
	}
}
