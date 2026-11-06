package securedb

import (
	"context"
	"database/sql"
	"fmt"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// CheckpointTruncate flushes the WAL before a caller closes or replaces a
// database file. SQLite reports a blocked checkpoint in the result row, not
// necessarily as an SQL execution error; Exec alone silently loses that status.
func CheckpointTruncate(ctx context.Context, db *sql.DB) error {
	var busy, logged, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logged, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint blocked (%d of %d frames flushed): %w", checkpointed, logged, sqlite3.Error{Code: sqlite3.ErrBusy})
	}
	return nil
}
