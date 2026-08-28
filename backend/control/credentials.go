package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"omni_money/backend/keyenvelope"
)

// RecordSuccessfulLogin commits a password login only if the exact credential
// verified by the caller is still current and the user is still active. The
// expensive password KDF must run before this call; matching the envelope and
// updated-at revision inside one immediate transaction prevents a password
// change or recovery reset that wins that race from being followed by a login
// using the stale password credential.
func (s *Store) RecordSuccessfulLogin(
	ctx context.Context,
	userID string,
	expected PasswordCredential,
	now time.Time,
) error {
	userID, err := normalizeID(userID)
	if err != nil {
		return err
	}
	if expected.UserID != userID {
		return ErrCredentialConflict
	}
	expectedJSON, err := encodeKeyEnvelope(expected.Envelope, keyenvelope.KindPassword)
	if err != nil {
		return fmt.Errorf("expected password credential: %w", err)
	}
	expectedUpdatedAt, err := validateOperationTime(expected.UpdatedAt)
	if err != nil {
		return fmt.Errorf("expected password credential revision: %w", err)
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return err
	}
	if now.Before(expectedUpdatedAt) {
		return fmt.Errorf("%w: login time predates the credential revision", ErrCredentialConflict)
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveUser(ctx, connection, userID); err != nil {
			return err
		}
		var credentialMatches bool
		if err := connection.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM password_credentials
			WHERE user_id = ? AND envelope_json = ? AND updated_at_ms = ?
		)`, userID, expectedJSON, expectedUpdatedAt.UnixMilli()).Scan(&credentialMatches); err != nil {
			return fmt.Errorf("verify successful login credential revision: %w", err)
		}
		if !credentialMatches {
			return ErrCredentialConflict
		}
		result, err := connection.ExecContext(ctx, `UPDATE users
			SET last_login_at_ms = CASE
				WHEN last_login_at_ms IS NULL OR last_login_at_ms < ? THEN ?
				ELSE last_login_at_ms
			END
			WHERE id = ? AND state = 'active' AND created_at_ms <= ?`,
			now.UnixMilli(), now.UnixMilli(), userID, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("record successful login: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify successful login update: %w", err)
		}
		if updated != 1 {
			return errors.New("successful login time predates account creation")
		}
		return nil
	})
}

// ReplacePasswordCredential uses the exact stored envelope and its updated-at
// revision as a compare-and-swap token. The caller must wrap replacement with
// the same UserID/VaultID context after proving the current password.
func (s *Store) ReplacePasswordCredential(
	ctx context.Context,
	userID string,
	expected PasswordCredential,
	replacement PasswordCredentialInput,
	now time.Time,
) error {
	userID, err := normalizeID(userID)
	if err != nil {
		return err
	}
	if expected.UserID != userID {
		return ErrCredentialConflict
	}
	expectedJSON, err := encodeKeyEnvelope(expected.Envelope, keyenvelope.KindPassword)
	if err != nil {
		return fmt.Errorf("expected password credential: %w", err)
	}
	replacementJSON, err := encodeKeyEnvelope(replacement.Envelope, keyenvelope.KindPassword)
	if err != nil {
		return fmt.Errorf("replacement password credential: %w", err)
	}
	if expectedJSON == replacementJSON {
		return errors.New("replacement password credential must differ from the current credential")
	}
	expectedUpdatedAt, err := validateOperationTime(expected.UpdatedAt)
	if err != nil {
		return fmt.Errorf("expected password credential revision: %w", err)
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return err
	}
	if now.UnixMilli() <= expectedUpdatedAt.UnixMilli() {
		return fmt.Errorf("%w: replacement time must advance the credential revision", ErrCredentialConflict)
	}

	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveUser(ctx, connection, userID); err != nil {
			return err
		}
		result, err := connection.ExecContext(ctx, `UPDATE password_credentials
			SET envelope_json = ?, updated_at_ms = ?
			WHERE user_id = ? AND envelope_json = ? AND updated_at_ms = ?`,
			replacementJSON, now.UnixMilli(), userID, expectedJSON, expectedUpdatedAt.UnixMilli())
		if err != nil {
			return fmt.Errorf("replace password credential: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify password credential replacement: %w", err)
		}
		if updated != 1 {
			return ErrCredentialConflict
		}
		return advanceUserUpdatedAt(ctx, connection, userID, now)
	})
}

// RotateRecoveryEnvelope atomically revokes the expected active envelope and
// installs one replacement. Exact envelope/ID/creation-time matching prevents
// two recovery rotations from both succeeding after the same preflight.
func (s *Store) RotateRecoveryEnvelope(
	ctx context.Context,
	userID string,
	expected RecoveryEnvelope,
	replacement RecoveryEnvelopeInput,
	now time.Time,
) (RecoveryEnvelope, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if expected.UserID != userID || expected.State != RecoveryEnvelopeActive {
		return RecoveryEnvelope{}, ErrRecoveryConflict
	}
	expectedID, err := normalizeID(expected.ID)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("expected recovery envelope id: %w", err)
	}
	expectedJSON, err := encodeKeyEnvelope(expected.Envelope, keyenvelope.KindRecovery)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("expected recovery envelope: %w", err)
	}
	expectedCreatedAt, err := validateOperationTime(expected.CreatedAt)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("expected recovery envelope revision: %w", err)
	}
	replacementID, err := idOrGenerate(replacement.ID)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("replacement recovery envelope id: %w", err)
	}
	if replacementID == expectedID {
		return RecoveryEnvelope{}, fmt.Errorf("%w: replacement recovery envelope must use a new id", ErrRecoveryConflict)
	}
	replacementJSON, err := encodeKeyEnvelope(replacement.Envelope, keyenvelope.KindRecovery)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("replacement recovery envelope: %w", err)
	}
	if replacementJSON == expectedJSON {
		return RecoveryEnvelope{}, fmt.Errorf("%w: replacement recovery envelope must differ from the current envelope", ErrRecoveryConflict)
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	if now.Before(expectedCreatedAt) {
		return RecoveryEnvelope{}, fmt.Errorf("%w: rotation time predates the current envelope", ErrRecoveryConflict)
	}
	storedReplacement, err := decodeKeyEnvelope(replacementJSON, keyenvelope.KindRecovery)
	if err != nil {
		return RecoveryEnvelope{}, err
	}

	result := RecoveryEnvelope{
		ID:        replacementID,
		UserID:    userID,
		Envelope:  storedReplacement,
		State:     RecoveryEnvelopeActive,
		CreatedAt: now,
	}
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveUser(ctx, connection, userID); err != nil {
			return err
		}
		updated, err := revokeExpectedRecoveryEnvelope(
			ctx, connection, userID, expectedID, expectedJSON, expectedCreatedAt, now,
		)
		if err != nil {
			return err
		}
		if !updated {
			return ErrRecoveryConflict
		}
		if err := insertActiveRecoveryEnvelope(ctx, connection, result.ID, userID, replacementJSON, now); err != nil {
			return err
		}
		return advanceUserUpdatedAt(ctx, connection, userID, now)
	})
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	return result, nil
}

func requireActiveUser(ctx context.Context, connection *sql.Conn, userID string) error {
	_, state, err := loadRoleAndState(ctx, connection, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		return err
	}
	if state != UserActive {
		return ErrForbidden
	}
	return nil
}

func revokeExpectedRecoveryEnvelope(
	ctx context.Context,
	connection *sql.Conn,
	userID, envelopeID, envelopeJSON string,
	createdAt, now time.Time,
) (bool, error) {
	result, err := connection.ExecContext(ctx, `UPDATE recovery_envelopes
		SET state = 'revoked', revoked_at_ms = ?
		WHERE id = ? AND user_id = ? AND state = 'active'
			AND envelope_json = ? AND created_at_ms = ?`,
		now.UnixMilli(), envelopeID, userID, envelopeJSON, createdAt.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("revoke recovery envelope: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("verify recovery envelope revocation: %w", err)
	}
	return updated == 1, nil
}

func insertActiveRecoveryEnvelope(
	ctx context.Context,
	connection *sql.Conn,
	envelopeID, userID, envelopeJSON string,
	now time.Time,
) error {
	_, err := connection.ExecContext(ctx, `INSERT INTO recovery_envelopes(
		id, user_id, envelope_json, state, created_at_ms
	) VALUES (?, ?, ?, 'active', ?)`, envelopeID, userID, envelopeJSON, now.UnixMilli())
	if isSQLiteConstraint(err) {
		return fmt.Errorf("%w: recovery envelope id or active state already exists", ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("insert active recovery envelope: %w", err)
	}
	return nil
}

func advanceUserUpdatedAt(ctx context.Context, connection *sql.Conn, userID string, now time.Time) error {
	_, err := connection.ExecContext(ctx, `UPDATE users
		SET updated_at_ms = CASE WHEN updated_at_ms < ? THEN ? ELSE updated_at_ms END
		WHERE id = ?`, now.UnixMilli(), now.UnixMilli(), userID)
	if err != nil {
		return fmt.Errorf("advance user update time: %w", err)
	}
	return nil
}
