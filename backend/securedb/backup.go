package securedb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"omni_money/backend/fileprivacy"
)

// Backup creates a consistent SQLite online backup. The destination is opened
// through the same Opener, so an encrypted source can never produce a
// plaintext snapshot by accident.
func (o *Opener) Backup(ctx context.Context, source *sql.DB, snapshotPath string) (err error) {
	if source == nil {
		return fmt.Errorf("source database is not open")
	}
	if err := o.CheckIntegrity(ctx, source); err != nil {
		return err
	}
	placeholder, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- caller generates this path inside the private snapshot directory.
	if err != nil {
		return err
	}
	if err := fileprivacy.Harden(placeholder); err != nil {
		_ = placeholder.Close()
		_ = os.Remove(snapshotPath)
		return err
	}
	placeholderInfo, err := placeholder.Stat()
	if err != nil {
		_ = placeholder.Close()
		_ = os.Remove(snapshotPath)
		return err
	}
	succeeded := false
	defer func() {
		_ = placeholder.Close()
		if !succeeded {
			_ = os.Remove(snapshotPath)
		}
	}()

	destination, err := o.Open(ctx, snapshotPath, Snapshot)
	if err != nil {
		return err
	}
	defer func() { _ = destination.Close() }()
	assertDestination := func() error {
		info, statErr := os.Stat(snapshotPath)
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(placeholderInfo, info) {
			return fmt.Errorf("snapshot destination was replaced during backup")
		}
		return nil
	}
	if err := assertDestination(); err != nil {
		return err
	}

	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	if err := assertDestination(); err != nil {
		return err
	}
	defer sourceConn.Close()
	destinationConn, err := destination.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()

	err = destinationConn.Raw(func(destinationDriverConn any) error {
		destinationSQLiteConn, ok := destinationDriverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected destination SQLite connection")
		}
		return sourceConn.Raw(func(sourceDriverConn any) error {
			sourceSQLiteConn, ok := sourceDriverConn.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected source SQLite connection")
			}
			backup, err := destinationSQLiteConn.Backup("main", sourceSQLiteConn, "main")
			if err != nil {
				return err
			}
			for {
				done, stepErr := backup.Step(128)
				if stepErr != nil {
					_ = backup.Finish()
					return stepErr
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					_ = backup.Finish()
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	})
	if err != nil {
		return err
	}
	// Release the exact connections used by sqlite3_backup before reopening
	// the destination for verification. This prevents a pooled connection from
	// observing pager state that predates backup.Finish().
	if err := destinationConn.Close(); err != nil {
		return err
	}
	if err := sourceConn.Close(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	verified, err := o.Open(ctx, snapshotPath, Snapshot)
	if err != nil {
		return err
	}
	defer verified.Close()
	if err := o.CheckIntegrity(ctx, verified); err != nil {
		return err
	}
	if err := os.Chmod(snapshotPath, 0600); err != nil {
		return err
	}
	if o.Encrypted() {
		if err := RequireEncryptedHeader(snapshotPath); err != nil {
			return err
		}
	}
	if err := assertDestination(); err != nil {
		return err
	}
	if err := placeholder.Close(); err != nil {
		return err
	}
	succeeded = true
	return nil
}
