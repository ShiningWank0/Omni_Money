package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const invitationColumns = `id, email, role, state, created_by, accepted_by,
	created_at_ms, expires_at_ms, resolved_at_ms`

func (s *Store) CreateInvitation(
	ctx context.Context,
	actorID string,
	input CreateInvitationInput,
	now time.Time,
) (Invitation, error) {
	actorID, err := normalizeID(actorID)
	if err != nil {
		return Invitation{}, fmt.Errorf("actor id: %w", err)
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return Invitation{}, err
	}
	id, err := idOrGenerate(input.ID)
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation id: %w", err)
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return Invitation{}, err
	}
	if err := validateRole(input.Role); err != nil {
		return Invitation{}, err
	}
	if err := validateTokenHash(input.TokenHash); err != nil {
		return Invitation{}, err
	}
	expiresAt, err := validateInvitationExpiry(now, input.ExpiresAt)
	if err != nil {
		return Invitation{}, err
	}
	result := Invitation{
		ID:        id,
		Email:     email,
		Role:      input.Role,
		State:     InvitationPending,
		CreatedBy: actorID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		var existingUser bool
		if err := connection.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM users WHERE email = ? COLLATE NOCASE)", email).Scan(&existingUser); err != nil {
			return fmt.Errorf("check invitation email: %w", err)
		}
		if existingUser {
			return fmt.Errorf("%w: email already belongs to a user", ErrConflict)
		}
		// Expired pending rows are resolved before the partial unique index is
		// evaluated, allowing a fresh invitation for the same email.
		if _, err := connection.ExecContext(ctx, `UPDATE invitations
			SET state = 'expired', resolved_at_ms = ?
			WHERE email = ? AND state = 'pending' AND expires_at_ms <= ?`,
			now.UnixMilli(), email, now.UnixMilli()); err != nil {
			return fmt.Errorf("expire prior invitation: %w", err)
		}
		_, err := connection.ExecContext(ctx, `INSERT INTO invitations(
			id, token_hash, email, role, state, created_by, created_at_ms, expires_at_ms
		) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)`,
			id, cloneBytes(input.TokenHash), email, string(input.Role), actorID,
			now.UnixMilli(), expiresAt.UnixMilli())
		if isSQLiteConstraint(err) {
			return fmt.Errorf("%w: invitation token, id, or pending email already exists", ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("insert invitation: %w", err)
		}
		return nil
	})
	return result, err
}

func (s *Store) GetInvitationByTokenHash(ctx context.Context, tokenHash []byte) (Invitation, error) {
	if err := validateTokenHash(tokenHash); err != nil {
		return Invitation{}, err
	}
	db, err := s.database()
	if err != nil {
		return Invitation{}, err
	}
	result, err := scanInvitation(db.QueryRowContext(ctx,
		`SELECT `+invitationColumns+` FROM invitations WHERE token_hash = ?`, cloneBytes(tokenHash)))
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("get invitation: %w", err)
	}
	return result, nil
}

func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+invitationColumns+`
		FROM invitations ORDER BY created_at_ms DESC, id LIMIT 500`)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()
	result := make([]Invitation, 0)
	for rows.Next() {
		invitation, err := scanInvitation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		result = append(result, invitation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invitations: %w", err)
	}
	return result, nil
}

func (s *Store) AcceptInvitation(
	ctx context.Context,
	tokenHash []byte,
	input NewUserInput,
	now time.Time,
) (UserSummary, error) {
	if err := validateTokenHash(tokenHash); err != nil {
		return UserSummary{}, err
	}
	now, err := validateOperationTime(now)
	if err != nil {
		return UserSummary{}, err
	}
	prepared, err := prepareNewUser(input, now)
	if err != nil {
		return UserSummary{}, err
	}
	var result UserSummary
	var semanticError error
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		invitation, err := scanInvitation(connection.QueryRowContext(ctx,
			`SELECT `+invitationColumns+` FROM invitations WHERE token_hash = ?`, cloneBytes(tokenHash)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load invitation for acceptance: %w", err)
		}
		if invitation.State != InvitationPending {
			return ErrInvitationInactive
		}
		if !invitation.ExpiresAt.After(now) {
			if _, err := connection.ExecContext(ctx, `UPDATE invitations
				SET state = 'expired', resolved_at_ms = ? WHERE id = ? AND state = 'pending'`,
				now.UnixMilli(), invitation.ID); err != nil {
				return fmt.Errorf("expire invitation: %w", err)
			}
			semanticError = ErrInvitationExpired
			return nil
		}
		if prepared.email != invitation.Email {
			return errors.New("account email does not match invitation")
		}
		if err := insertPreparedUser(ctx, connection, prepared, invitation.Role, now); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `UPDATE invitations
			SET state = 'accepted', accepted_by = ?, resolved_at_ms = ?
			WHERE id = ? AND state = 'pending'`,
			prepared.id, now.UnixMilli(), invitation.ID); err != nil {
			return fmt.Errorf("accept invitation: %w", err)
		}
		result = prepared.summary(invitation.Role, now)
		return nil
	})
	if err != nil {
		return UserSummary{}, err
	}
	if semanticError != nil {
		return UserSummary{}, semanticError
	}
	return result, nil
}

func (s *Store) RevokeInvitation(ctx context.Context, actorID, invitationID string, now time.Time) error {
	actorID, invitationID, now, err := validateUserMutation(actorID, invitationID, now)
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		var state InvitationState
		if err := connection.QueryRowContext(ctx,
			"SELECT state FROM invitations WHERE id = ?", invitationID).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load invitation state: %w", err)
		}
		if state != InvitationPending {
			return ErrInvitationInactive
		}
		_, err := connection.ExecContext(ctx, `UPDATE invitations
			SET state = 'revoked', resolved_at_ms = ? WHERE id = ? AND state = 'pending'`,
			now.UnixMilli(), invitationID)
		return err
	})
}

func scanInvitation(scanner rowScanner) (Invitation, error) {
	var result Invitation
	var acceptedBy sql.NullString
	var createdAt, expiresAt int64
	var resolvedAt sql.NullInt64
	if err := scanner.Scan(
		&result.ID,
		&result.Email,
		&result.Role,
		&result.State,
		&result.CreatedBy,
		&acceptedBy,
		&createdAt,
		&expiresAt,
		&resolvedAt,
	); err != nil {
		return Invitation{}, err
	}
	if acceptedBy.Valid {
		value := acceptedBy.String
		result.AcceptedBy = &value
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	if resolvedAt.Valid {
		value := time.UnixMilli(resolvedAt.Int64).UTC()
		result.ResolvedAt = &value
	}
	return result, nil
}
