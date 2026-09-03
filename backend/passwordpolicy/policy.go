// Package passwordpolicy defines the single password boundary shared by
// Desktop and multi-user server authentication. Length is measured in UTF-8
// bytes because the Go/Wails and JSON boundaries both transmit UTF-8.
package passwordpolicy

import "errors"

const (
	MinimumBytes = 12
	MaximumBytes = 1024
)

var ErrInvalid = errors.New("password must contain between 12 and 1024 UTF-8 bytes")

func Validate(password []byte) error {
	if len(password) < MinimumBytes || len(password) > MaximumBytes {
		return ErrInvalid
	}
	return nil
}
