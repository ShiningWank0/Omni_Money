//go:build !windows && !linux && !darwin

package database

import "errors"

func publishSnapshotPruneManifestNoReplace(_, _ string) error {
	return errors.New("atomic no-replace snapshot prune journal publication is unsupported")
}
