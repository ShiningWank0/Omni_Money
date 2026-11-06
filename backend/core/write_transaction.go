package core

import (
	"context"
	"database/sql"
	"fmt"
)

// beginLedgerWrite reserves SQLite's single writer before the caller reads
// any ledger state. A deferred read transaction cannot reliably wait when it
// later upgrades to a writer: its snapshot may already be obsolete.
//
// The zero-row DELETE acquires the same writer reservation as BEGIN IMMEDIATE,
// without changing data or firing row triggers. This retains database/sql's
// *sql.Tx ownership, cancellation and prepared-statement cleanup, and works on
// both SQLite and SQLCipher without changing read-only transaction behavior.
// SQLite coordinates the reservation per database, including other processes.
func beginLedgerWrite(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM transaction_links WHERE 0"); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("reserve ledger writer: %w", err)
	}
	return tx, nil
}
