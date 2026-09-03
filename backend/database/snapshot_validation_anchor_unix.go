//go:build !windows

package database

import (
	"errors"
	"fmt"
	"os"
)

// openSnapshotValidationAnchor opens the candidate through the pinned root.
// SQLite is then pointed at the descriptor path, so pathname replacement or
// an ABA swap cannot make it validate a different inode.
func openSnapshotValidationAnchor(lock *snapshotTransactionLock, path string) (*os.File, string, error) {
	file, err := lock.openArtifact(path)
	if err != nil {
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	var pathErrors []error
	for _, directory := range []string{"/proc/self/fd", "/dev/fd"} {
		descriptorPath := fmt.Sprintf("%s/%d", directory, file.Fd())
		probe, statErr := os.Open(descriptorPath) // #nosec G304 -- generated from this process's live descriptor.
		if statErr != nil {
			pathErrors = append(pathErrors, fmt.Errorf("%s: %w", descriptorPath, statErr))
			continue
		}
		descriptorInfo, statErr := probe.Stat()
		closeErr := probe.Close()
		if statErr == nil && closeErr == nil && os.SameFile(info, descriptorInfo) {
			return file, descriptorPath, nil
		}
		if statErr != nil {
			pathErrors = append(pathErrors, fmt.Errorf("%s: %w", descriptorPath, statErr))
		} else if closeErr != nil {
			pathErrors = append(pathErrors, fmt.Errorf("%s: %w", descriptorPath, closeErr))
		} else {
			pathErrors = append(pathErrors, fmt.Errorf("%s: descriptor identity mismatch", descriptorPath))
		}
	}
	_ = file.Close()
	return nil, "", errors.Join(append([]error{errors.New("stable snapshot validation descriptor path is unavailable")}, pathErrors...)...)
}
