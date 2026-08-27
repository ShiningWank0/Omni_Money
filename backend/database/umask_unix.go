//go:build !windows

package database

import "syscall"

func setRestrictiveUmask() {
	syscall.Umask(0077)
}
