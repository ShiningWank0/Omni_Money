package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"omni_money/backend/keyenvelope"
)

const passwordResetColumns = `id, user_id, state, created_by,
	created_at_ms, expires_at_ms, resolved_at_ms` // #nosec G101 -- static SQL column projection, not a credential.

// ListPasswordResetTickets returns only administrative metadata. Bearer-token
// hashes and every credential envelope are deliberately excluded by the
// explicit projection above.
func (s *Store) ListPasswordResetTickets(ctx context.Context) ([]PasswordResetTicket, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+passwordResetColumns+`
		FROM password_reset_tickets ORDER BY created_at_ms DESC, id LIMIT 500`)
	if err != nil {
		return nil, fmt.Errorf("list password reset tickets: %w", err)
	}
	defer rows.Close()
	result := make([]PasswordResetTicket, 0)
	for rows.Next() {
		ticket, err := scanPasswordResetTicket(rows)
		if err != nil {
			return nil, fmt.Errorf("scan password reset ticket: %w", err)
		}
		result = append(result, ticket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate password reset tickets: %w", err)
	}
	return result, nil
}

func (s *Store) CreatePasswordResetTicket(
	ctx context.Context,
	actorID, targetUserID string,
	input CreatePasswordResetTicketInput,
	now time.Time,
) (PasswordResetTicket, error) {
	actorID, targetUserID, now, err := validateUserMutation(actorID, targetUserID, now)
	if err != nil {
		return PasswordResetTicket{}, err
	}
	id, err := idOrGenerate(input.ID)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("password reset ticket id: %w", err)
	}
	if err := validateTokenHash(input.TokenHash); err != nil {
		return PasswordResetTicket{}, err
	}
	expiresAt, err := validatePasswordResetExpiry(now, input.ExpiresAt)
	if err != nil {
		return PasswordResetTicket{}, err
	}
	result := PasswordResetTicket{
		ID:        id,
		UserID:    targetUserID,
		State:     PasswordResetPending,
		CreatedBy: actorID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		_, targetState, err := loadRoleAndState(ctx, connection, targetUserID)
		if err != nil {
			return err
		}
		if targetState != UserActive {
			return ErrForbidden
		}
		// A new ticket invalidates every older bearer for the same user. Already
		// expired records retain the more precise expired state; otherwise they
		// are explicitly revoked.
		if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
			SET state = CASE WHEN expires_at_ms <= ? THEN 'expired' ELSE 'revoked' END,
				resolved_at_ms = ?
			WHERE user_id = ? AND state = 'pending'`,
			now.UnixMilli(), now.UnixMilli(), targetUserID); err != nil {
			return fmt.Errorf("invalidate prior password reset tickets: %w", err)
		}
		_, err = connection.ExecContext(ctx, `INSERT INTO password_reset_tickets(
			id, user_id, token_hash, state, created_by, created_at_ms, expires_at_ms
		) VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
			id, targetUserID, cloneBytes(input.TokenHash), actorID,
			now.UnixMilli(), expiresAt.UnixMilli())
		if isSQLiteConstraint(err) {
			return fmt.Errorf("%w: password reset token or id already exists", ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("insert password reset ticket: %w", err)
		}
		return nil
	})
	return result, err
}

// GetPasswordResetTicketByTokenHash resolves reset preflight state without
// consuming it. The returned DTO intentionally has no token-hash field.
func (s *Store) GetPasswordResetTicketByTokenHash(
	ctx context.Context,
	tokenHash []byte,
) (PasswordResetTicket, error) {
	if err := validateTokenHash(tokenHash); err != nil {
		return PasswordResetTicket{}, err
	}
	db, err := s.database()
	if err != nil {
		return PasswordResetTicket{}, err
	}
	ticket, err := scanPasswordResetTicket(db.QueryRowContext(ctx,
		`SELECT `+passwordResetColumns+` FROM password_reset_tickets WHERE token_hash = ?`,
		cloneBytes(tokenHash)))
	if errors.Is(err, sql.ErrNoRows) {
		return PasswordResetTicket{}, ErrNotFound
	}
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("get password reset ticket: %w", err)
	}
	return ticket, nil
}

// CompletePasswordReset commits the whole recovery transition atomically.
// Recovery-secret authentication happens in the service before this call; the
// expected envelope is still compared exactly to prevent a preflight/commit
// race. Replacement envelopes must use the unchanged UserID/VaultID context.
func (s *Store) CompletePasswordReset(
	ctx context.Context,
	input CompletePasswordResetInput,
	now time.Time,
) (PasswordResetTicket, error) {
	if err := validateTokenHash(input.TokenHash); err != nil {
		return PasswordResetTicket{}, err
	}
	expected := input.ExpectedRecoveryEnvelope
	expectedUserID, err := normalizeID(expected.UserID)
	if err != nil || expected.State != RecoveryEnvelopeActive {
		return PasswordResetTicket{}, ErrRecoveryConflict
	}
	expectedID, err := normalizeID(expected.ID)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("expected recovery envelope id: %w", err)
	}
	expectedJSON, err := encodeKeyEnvelope(expected.Envelope, keyenvelope.KindRecovery)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("expected recovery envelope: %w", err)
	}
	expectedCreatedAt, err := validateOperationTime(expected.CreatedAt)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("expected recovery envelope revision: %w", err)
	}
	passwordJSON, err := encodeKeyEnvelope(input.PasswordCredential.Envelope, keyenvelope.KindPassword)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("replacement password credential: %w", err)
	}
	replacementRecoveryID, err := idOrGenerate(input.RecoveryEnvelope.ID)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("replacement recovery envelope id: %w", err)
	}
	if replacementRecoveryID == expectedID {
		return PasswordResetTicket{}, errors.New("replacement recovery envelope must use a new id")
	}
	recoveryJSON, err := encodeKeyEnvelope(input.RecoveryEnvelope.Envelope, keyenvelope.KindRecovery)
	if err != nil {
		return PasswordResetTicket{}, fmt.Errorf("replacement recovery envelope: %w", err)
	}
	if recoveryJSON == expectedJSON {
		return PasswordResetTicket{}, errors.New("replacement recovery envelope must differ from the current envelope")
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return PasswordResetTicket{}, err
	}
	if now.Before(expectedCreatedAt) {
		return PasswordResetTicket{}, fmt.Errorf("%w: reset time predates the current recovery envelope", ErrRecoveryConflict)
	}

	var result PasswordResetTicket
	var semanticError error
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		ticket, err := scanPasswordResetTicket(connection.QueryRowContext(ctx,
			`SELECT `+passwordResetColumns+` FROM password_reset_tickets WHERE token_hash = ?`,
			cloneBytes(input.TokenHash)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load password reset ticket for completion: %w", err)
		}
		if ticket.State != PasswordResetPending {
			return ErrResetTicketInactive
		}
		if !ticket.ExpiresAt.After(now) {
			if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
				SET state = 'expired', resolved_at_ms = ?
				WHERE id = ? AND state = 'pending'`, now.UnixMilli(), ticket.ID); err != nil {
				return fmt.Errorf("expire password reset ticket: %w", err)
			}
			semanticError = ErrResetTicketExpired
			return nil
		}
		if ticket.UserID != expectedUserID {
			return ErrRecoveryConflict
		}
		if err := requireActiveUser(ctx, connection, ticket.UserID); err != nil {
			return err
		}

		// Recovery proves possession of the offline recovery secret, not of any
		// registered WebAuthn authenticator. Revoke every passkey in the same
		// IMMEDIATE transaction as both envelope replacements and ticket
		// consumption so no old authentication path survives a reset and no
		// partial revocation can commit on a later failure.
		if _, err := connection.ExecContext(ctx,
			"DELETE FROM passkey_credentials WHERE user_id = ?", ticket.UserID); err != nil {
			return fmt.Errorf("revoke passkeys during password reset: %w", err)
		}

		passwordUpdate, err := connection.ExecContext(ctx, `UPDATE password_credentials
			SET envelope_json = ?, updated_at_ms = ?
			WHERE user_id = ? AND updated_at_ms < ?`,
			passwordJSON, now.UnixMilli(), ticket.UserID, now.UnixMilli())
		if err != nil {
			return fmt.Errorf("replace password credential during reset: %w", err)
		}
		passwordRows, err := passwordUpdate.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify password reset credential update: %w", err)
		}
		if passwordRows != 1 {
			return ErrCredentialConflict
		}

		revoked, err := revokeExpectedRecoveryEnvelope(
			ctx, connection, ticket.UserID, expectedID, expectedJSON, expectedCreatedAt, now,
		)
		if err != nil {
			return err
		}
		if !revoked {
			return ErrRecoveryConflict
		}
		if err := insertActiveRecoveryEnvelope(
			ctx, connection, replacementRecoveryID, ticket.UserID, recoveryJSON, now,
		); err != nil {
			return err
		}

		consumed, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
			SET state = 'consumed', resolved_at_ms = ?
			WHERE id = ? AND state = 'pending'`, now.UnixMilli(), ticket.ID)
		if err != nil {
			return fmt.Errorf("consume completed password reset ticket: %w", err)
		}
		consumedRows, err := consumed.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify completed password reset ticket: %w", err)
		}
		if consumedRows != 1 {
			return ErrResetTicketInactive
		}
		if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
			SET state = 'revoked', resolved_at_ms = ?
			WHERE user_id = ? AND state = 'pending' AND id <> ?`,
			now.UnixMilli(), ticket.UserID, ticket.ID); err != nil {
			return fmt.Errorf("revoke sibling password reset tickets: %w", err)
		}
		if err := advanceUserUpdatedAt(ctx, connection, ticket.UserID, now); err != nil {
			return err
		}
		ticket.State = PasswordResetConsumed
		resolvedAt := now
		ticket.ResolvedAt = &resolvedAt
		result = ticket
		return nil
	})
	if err != nil {
		return PasswordResetTicket{}, err
	}
	if semanticError != nil {
		return PasswordResetTicket{}, semanticError
	}
	return result, nil
}

func (s *Store) RevokePasswordResetTicket(
	ctx context.Context,
	actorID, ticketID string,
	now time.Time,
) error {
	actorID, ticketID, now, err := validateUserMutation(actorID, ticketID, now)
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		var state PasswordResetState
		if err := connection.QueryRowContext(ctx,
			"SELECT state FROM password_reset_tickets WHERE id = ?", ticketID).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load password reset ticket state: %w", err)
		}
		if state != PasswordResetPending {
			return ErrResetTicketInactive
		}
		_, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
			SET state = 'revoked', resolved_at_ms = ? WHERE id = ? AND state = 'pending'`,
			now.UnixMilli(), ticketID)
		return err
	})
}

func scanPasswordResetTicket(scanner rowScanner) (PasswordResetTicket, error) {
	var result PasswordResetTicket
	var createdAt, expiresAt int64
	var resolvedAt sql.NullInt64
	if err := scanner.Scan(
		&result.ID,
		&result.UserID,
		&result.State,
		&result.CreatedBy,
		&createdAt,
		&expiresAt,
		&resolvedAt,
	); err != nil {
		return PasswordResetTicket{}, err
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	if resolvedAt.Valid {
		value := time.UnixMilli(resolvedAt.Int64).UTC()
		result.ResolvedAt = &value
	}
	return result, nil
}
