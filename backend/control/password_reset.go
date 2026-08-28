package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const passwordResetColumns = `id, user_id, state, created_by,
	created_at_ms, expires_at_ms, resolved_at_ms` // #nosec G101 -- static SQL column projection, not a credential.

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
	expiresAt, err := validateExpiry(now, input.ExpiresAt)
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
		if _, _, err := loadRoleAndState(ctx, connection, targetUserID); err != nil {
			return err
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
		_, err := connection.ExecContext(ctx, `INSERT INTO password_reset_tickets(
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

// ConsumePasswordResetTicket marks the bearer token as single-use and returns
// only its target. It does not replace credentials or unwrap a vault key; the
// service layer must still require a valid recovery proof before doing either.
func (s *Store) ConsumePasswordResetTicket(
	ctx context.Context,
	tokenHash []byte,
	now time.Time,
) (PasswordResetTicket, error) {
	if err := validateTokenHash(tokenHash); err != nil {
		return PasswordResetTicket{}, err
	}
	now, err := validateOperationTime(now)
	if err != nil {
		return PasswordResetTicket{}, err
	}
	var result PasswordResetTicket
	var semanticError error
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		ticket, err := scanPasswordResetTicket(connection.QueryRowContext(ctx,
			`SELECT `+passwordResetColumns+` FROM password_reset_tickets WHERE token_hash = ?`,
			cloneBytes(tokenHash)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load password reset ticket: %w", err)
		}
		if ticket.State != PasswordResetPending {
			return ErrResetTicketInactive
		}
		if !ticket.ExpiresAt.After(now) {
			if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
				SET state = 'expired', resolved_at_ms = ? WHERE id = ? AND state = 'pending'`,
				now.UnixMilli(), ticket.ID); err != nil {
				return fmt.Errorf("expire password reset ticket: %w", err)
			}
			semanticError = ErrResetTicketExpired
			return nil
		}
		if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
			SET state = 'consumed', resolved_at_ms = ? WHERE id = ? AND state = 'pending'`,
			now.UnixMilli(), ticket.ID); err != nil {
			return fmt.Errorf("consume password reset ticket: %w", err)
		}
		ticket.State = PasswordResetConsumed
		resolved := now
		ticket.ResolvedAt = &resolved
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
