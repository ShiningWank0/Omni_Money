package passwordpolicy

import (
	"bytes"
	"errors"
	"testing"
)

func TestValidateUTF8ByteBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		password []byte
		valid    bool
	}{
		{name: "eleven", password: bytes.Repeat([]byte{'x'}, 11)},
		{name: "twelve", password: bytes.Repeat([]byte{'x'}, 12), valid: true},
		{name: "257", password: bytes.Repeat([]byte{'x'}, 257), valid: true},
		{name: "1024", password: bytes.Repeat([]byte{'x'}, 1024), valid: true},
		{name: "1025", password: bytes.Repeat([]byte{'x'}, 1025)},
		{name: "four multibyte runes are twelve bytes", password: []byte("長長長長"), valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(test.password)
			if test.valid && err != nil {
				t.Fatalf("valid password rejected: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid password error = %v", err)
			}
		})
	}
}
