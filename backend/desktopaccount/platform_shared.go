package desktopaccount

import "errors"

const migrationLockFileName = ".desktop-migration.lock"

// ErrBusy is shared with the platform migration lock implementations, which
// are compiled and exercised independently of the CGO-backed database layer on
// native Windows CI.
var ErrBusy = errors.New("desktop account lifecycle change is in progress")
