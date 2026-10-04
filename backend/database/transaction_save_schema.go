package database

import (
	"context"
	"fmt"
	"strings"
)

const TransactionSaveTableSQL = `CREATE TABLE IF NOT EXISTS transaction_save_requests (
	request_id TEXT PRIMARY KEY NOT NULL CHECK(length(request_id) = 36),
	target_id INTEGER NOT NULL CHECK(target_id >= 0),
	request_hash BLOB NOT NULL CHECK(length(request_hash) = 32),
	payload BLOB CHECK(payload IS NULL OR length(payload) BETWEEN 2 AND 33554432),
	state TEXT NOT NULL CHECK(state IN ('pending', 'completed', 'failed')),
	result TEXT CHECK(result IS NULL OR length(result) BETWEEN 2 AND 33554432),
	error TEXT CHECK(error IS NULL OR length(error) BETWEEN 1 AND 256),
	created_at TEXT NOT NULL,
	CHECK((state = 'pending' AND payload IS NOT NULL AND result IS NULL AND error IS NULL)
	 OR (state = 'completed' AND payload IS NULL AND result IS NOT NULL AND error IS NULL)
	 OR (state = 'failed' AND result IS NULL AND error IS NOT NULL))
)`

const transactionSaveIndexSQL = `CREATE INDEX IF NOT EXISTS idx_transaction_saves_pending ON transaction_save_requests(state) WHERE state = 'pending'`

func validateTransactionSaveSchema(ctx context.Context, ctxQuery schemaQueryer) error {
	var definition string
	if err := ctxQuery.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='transaction_save_requests'").Scan(&definition); err != nil {
		return err
	}
	if canonicalDDL(definition) != canonicalDDL(strings.Replace(TransactionSaveTableSQL, " IF NOT EXISTS", "", 1)) {
		return fmt.Errorf("transaction save queue schema mismatch")
	}
	if err := ctxQuery.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_transaction_saves_pending'").Scan(&definition); err != nil {
		return err
	}
	if canonicalDDL(definition) != canonicalDDL(strings.Replace(transactionSaveIndexSQL, " IF NOT EXISTS", "", 1)) {
		return fmt.Errorf("transaction save queue index mismatch")
	}
	return nil
}
