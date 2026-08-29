package control

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 2

var schemaV1Statements = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 16 AND 128),
		email TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK(length(email) BETWEEN 3 AND 254),
		display_name TEXT NOT NULL CHECK(length(display_name) BETWEEN 1 AND 200),
		role TEXT NOT NULL CHECK(role IN ('admin', 'user')),
		state TEXT NOT NULL CHECK(state IN ('active', 'disabled')),
		vault_id TEXT NOT NULL UNIQUE CHECK(length(vault_id) BETWEEN 16 AND 128),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms >= created_at_ms),
		last_login_at_ms INTEGER,
		CHECK(last_login_at_ms IS NULL OR last_login_at_ms >= created_at_ms)
	) STRICT`,
	`CREATE TABLE IF NOT EXISTS password_credentials (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		envelope_json TEXT NOT NULL CHECK(length(envelope_json) BETWEEN 2 AND 8192 AND json_valid(envelope_json)),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms >= created_at_ms)
	) STRICT`,
	`CREATE TABLE IF NOT EXISTS recovery_envelopes (
		id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 16 AND 128),
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		envelope_json TEXT NOT NULL CHECK(length(envelope_json) BETWEEN 2 AND 8192 AND json_valid(envelope_json)),
		state TEXT NOT NULL CHECK(state IN ('active', 'revoked')),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		revoked_at_ms INTEGER,
		CHECK((state = 'active' AND revoked_at_ms IS NULL) OR
		      (state = 'revoked' AND revoked_at_ms IS NOT NULL AND revoked_at_ms >= created_at_ms))
	) STRICT`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_envelopes_one_active
	 ON recovery_envelopes(user_id) WHERE state = 'active'`,
	`CREATE TABLE IF NOT EXISTS invitations (
		id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 16 AND 128),
		token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash) = 32),
		email TEXT NOT NULL COLLATE NOCASE CHECK(length(email) BETWEEN 3 AND 254),
		role TEXT NOT NULL CHECK(role IN ('admin', 'user')),
		state TEXT NOT NULL CHECK(state IN ('pending', 'accepted', 'revoked', 'expired')),
		created_by TEXT NOT NULL REFERENCES users(id),
		accepted_by TEXT REFERENCES users(id),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		expires_at_ms INTEGER NOT NULL CHECK(expires_at_ms > created_at_ms),
		resolved_at_ms INTEGER,
		CHECK(resolved_at_ms IS NULL OR resolved_at_ms >= created_at_ms),
		CHECK((state = 'pending' AND accepted_by IS NULL AND resolved_at_ms IS NULL) OR
		      (state = 'accepted' AND accepted_by IS NOT NULL AND resolved_at_ms IS NOT NULL) OR
		      (state IN ('revoked', 'expired') AND accepted_by IS NULL AND resolved_at_ms IS NOT NULL))
	) STRICT`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_invitations_one_pending_email
	 ON invitations(email) WHERE state = 'pending'`,
	`CREATE INDEX IF NOT EXISTS idx_invitations_expiry
	 ON invitations(state, expires_at_ms)`,
	`CREATE TABLE IF NOT EXISTS password_reset_tickets (
		id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 16 AND 128),
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash) = 32),
		state TEXT NOT NULL CHECK(state IN ('pending', 'consumed', 'revoked', 'expired')),
		created_by TEXT NOT NULL REFERENCES users(id),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		expires_at_ms INTEGER NOT NULL CHECK(expires_at_ms > created_at_ms),
		resolved_at_ms INTEGER,
		CHECK(resolved_at_ms IS NULL OR resolved_at_ms >= created_at_ms),
		CHECK((state = 'pending' AND resolved_at_ms IS NULL) OR
		      (state IN ('consumed', 'revoked', 'expired') AND resolved_at_ms IS NOT NULL))
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_password_reset_user
	 ON password_reset_tickets(user_id, state)`,
	`CREATE INDEX IF NOT EXISTS idx_password_reset_expiry
	 ON password_reset_tickets(state, expires_at_ms)`,
}

var schemaV2Statements = []string{
	`CREATE TABLE IF NOT EXISTS passkey_credentials (
		credential_id BLOB PRIMARY KEY CHECK(length(credential_id) BETWEEN 16 AND 1024),
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
		credential_json TEXT NOT NULL CHECK(length(credential_json) BETWEEN 2 AND 1048576 AND json_valid(credential_json)),
		prf_salt BLOB NOT NULL CHECK(length(prf_salt) = 32),
		vault_envelope_json TEXT NOT NULL CHECK(length(vault_envelope_json) BETWEEN 2 AND 8192 AND json_valid(vault_envelope_json)),
		revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms >= created_at_ms),
		last_used_at_ms INTEGER,
		CHECK(last_used_at_ms IS NULL OR last_used_at_ms >= created_at_ms)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_passkey_credentials_user
	 ON passkey_credentials(user_id, created_at_ms)`,
}

func initializeSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read control schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("control schema version %d is newer than supported version %d", version, schemaVersion)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin control schema transaction: %w", err)
	}
	defer tx.Rollback()
	apply := func(statements []string, targetVersion int) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate control schema to version %d: %w", targetVersion, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", targetVersion)); err != nil {
			return fmt.Errorf("set control schema version %d: %w", targetVersion, err)
		}
		return nil
	}
	if version < 1 {
		if err := apply(schemaV1Statements, 1); err != nil {
			return err
		}
	}
	if version < 2 {
		if err := apply(schemaV2Statements, 2); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit control schema: %w", err)
	}
	return nil
}
