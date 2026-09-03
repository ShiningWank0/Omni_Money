//go:build !windows && !linux && !darwin

package database

import "errors"

func (lock *snapshotTransactionLock) publishManifestNoReplace(_, _ string) error {
	return errors.New("root-relative no-replace manifest publication is unsupported")
}
