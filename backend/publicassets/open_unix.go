//go:build !windows

package publicassets

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openNoFollow(root string, parts []string) (*os.File, error) {
	const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	directory, err := unix.Open(root, directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(directory) }()
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(directory, part, directoryFlags, 0)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(directory)
		directory = next
	}
	// NONBLOCK also keeps a substituted FIFO from hanging before fstat rejects it.
	descriptor, err := unix.Openat(directory, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), strings.Join(parts, "/"))
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, os.ErrInvalid
	}
	return file, nil
}
