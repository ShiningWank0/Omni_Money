package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"omni_money/backend/keyenvelope"
)

const userSummaryColumns = `id, email, display_name, role, state,
	created_at_ms, updated_at_ms, last_login_at_ms`

type rowScanner interface {
	Scan(...any) error
}

func scanUserSummary(scanner rowScanner) (UserSummary, error) {
	var result UserSummary
	var createdAt, updatedAt int64
	var lastLoginAt sql.NullInt64
	if err := scanner.Scan(
		&result.ID,
		&result.Email,
		&result.DisplayName,
		&result.Role,
		&result.State,
		&createdAt,
		&updatedAt,
		&lastLoginAt,
	); err != nil {
		return UserSummary{}, err
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if lastLoginAt.Valid {
		value := time.UnixMilli(lastLoginAt.Int64).UTC()
		result.LastLoginAt = &value
	}
	return result, nil
}

func isSQLiteConstraint(err error) bool {
	var sqliteError sqlite3.Error
	return errors.As(err, &sqliteError) && sqliteError.Code == sqlite3.ErrConstraint
}

// BootstrapFirstAdmin atomically creates the sole initial user as an active
// administrator. The transport/service layer must authorize this call with a
// deployment-local one-time setup secret; exposing it as an unguarded "first
// request wins" endpoint is unsafe.
func (s *Store) BootstrapFirstAdmin(ctx context.Context, input BootstrapAdminInput, now time.Time) (UserSummary, error) {
	now, err := validateOperationTime(now)
	if err != nil {
		return UserSummary{}, err
	}
	prepared, err := prepareNewUser(input, now)
	if err != nil {
		return UserSummary{}, err
	}
	var result UserSummary
	err = s.withImmediate(ctx, func(connection *sql.Conn) error {
		var count int
		if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
			return fmt.Errorf("count users for bootstrap: %w", err)
		}
		if count != 0 {
			return ErrAlreadyBootstrapped
		}
		if err := insertPreparedUser(ctx, connection, prepared, RoleAdmin, now); err != nil {
			return err
		}
		result = prepared.summary(RoleAdmin, now)
		return nil
	})
	return result, err
}

func (s *Store) IsBootstrapped(ctx context.Context) (bool, error) {
	db, err := s.database()
	if err != nil {
		return false, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users)").Scan(&exists); err != nil {
		return false, fmt.Errorf("check control bootstrap state: %w", err)
	}
	return exists, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	// This projection is intentionally explicit. Never replace it with SELECT *.
	rows, err := db.QueryContext(ctx, `SELECT `+userSummaryColumns+`
		FROM users ORDER BY email COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list control users: %w", err)
	}
	defer rows.Close()
	result := make([]UserSummary, 0)
	for rows.Next() {
		user, err := scanUserSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan control user: %w", err)
		}
		result = append(result, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate control users: %w", err)
	}
	return result, nil
}

func (s *Store) GetUser(ctx context.Context, id string) (UserSummary, error) {
	id, err := normalizeID(id)
	if err != nil {
		return UserSummary{}, err
	}
	db, err := s.database()
	if err != nil {
		return UserSummary{}, err
	}
	result, err := scanUserSummary(db.QueryRowContext(ctx,
		`SELECT `+userSummaryColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UserSummary{}, ErrNotFound
	}
	if err != nil {
		return UserSummary{}, fmt.Errorf("get control user: %w", err)
	}
	return result, nil
}

// GetUserByEmail is the authentication lookup counterpart to ListUsers. It
// returns only public account state; credential material must be fetched by ID
// through the explicitly sensitive GetPasswordCredential method.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (UserSummary, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return UserSummary{}, err
	}
	db, err := s.database()
	if err != nil {
		return UserSummary{}, err
	}
	result, err := scanUserSummary(db.QueryRowContext(ctx,
		`SELECT `+userSummaryColumns+` FROM users WHERE email = ? COLLATE NOCASE`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return UserSummary{}, ErrNotFound
	}
	if err != nil {
		return UserSummary{}, fmt.Errorf("get control user by email: %w", err)
	}
	return result, nil
}

// LookupVaultID is intentionally separate from UserSummary/ListUsers. Only a
// trusted vault-routing service should call it; vault.Manager derives the
// private filesystem path from this validated opaque identifier.
func (s *Store) LookupVaultID(ctx context.Context, userID string) (string, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return "", err
	}
	db, err := s.database()
	if err != nil {
		return "", err
	}
	var vaultID string
	if err := db.QueryRowContext(ctx, "SELECT vault_id FROM users WHERE id = ?", userID).Scan(&vaultID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lookup vault id: %w", err)
	}
	return vaultID, nil
}

func (s *Store) GetPasswordCredential(ctx context.Context, userID string) (PasswordCredential, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return PasswordCredential{}, err
	}
	db, err := s.database()
	if err != nil {
		return PasswordCredential{}, err
	}
	var result PasswordCredential
	var envelopeJSON string
	var createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, `SELECT user_id, envelope_json, created_at_ms, updated_at_ms
		FROM password_credentials WHERE user_id = ?`, userID).Scan(
		&result.UserID,
		&envelopeJSON,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PasswordCredential{}, ErrNotFound
	}
	if err != nil {
		return PasswordCredential{}, fmt.Errorf("get password credential: %w", err)
	}
	result.Envelope, err = decodeKeyEnvelope(envelopeJSON, keyenvelope.KindPassword)
	if err != nil {
		return PasswordCredential{}, fmt.Errorf("get password credential: %w", err)
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return result, nil
}

func (s *Store) GetActiveRecoveryEnvelope(ctx context.Context, userID string) (RecoveryEnvelope, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	db, err := s.database()
	if err != nil {
		return RecoveryEnvelope{}, err
	}
	var result RecoveryEnvelope
	var envelopeJSON string
	var createdAt int64
	var revokedAt sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT id, user_id, envelope_json,
		state, created_at_ms, revoked_at_ms
		FROM recovery_envelopes WHERE user_id = ? AND state = 'active'`, userID).Scan(
		&result.ID,
		&result.UserID,
		&envelopeJSON,
		&result.State,
		&createdAt,
		&revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryEnvelope{}, ErrNotFound
	}
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("get recovery envelope: %w", err)
	}
	result.Envelope, err = decodeKeyEnvelope(envelopeJSON, keyenvelope.KindRecovery)
	if err != nil {
		return RecoveryEnvelope{}, fmt.Errorf("get recovery envelope: %w", err)
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	if revokedAt.Valid {
		value := time.UnixMilli(revokedAt.Int64).UTC()
		result.RevokedAt = &value
	}
	return result, nil
}

func (s *Store) DisableUser(ctx context.Context, actorID, targetID string, now time.Time) error {
	actorID, targetID, now, err := validateUserMutation(actorID, targetID, now)
	if err != nil {
		return err
	}
	if actorID == targetID {
		return ErrSelfDisable
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		role, state, err := loadRoleAndState(ctx, connection, targetID)
		if err != nil {
			return err
		}
		if state == UserDisabled {
			return revokePendingCapabilities(ctx, connection, targetID, now)
		}
		if role == RoleAdmin {
			if err := requireAnotherActiveAdmin(ctx, connection, targetID); err != nil {
				return err
			}
		}
		_, err = connection.ExecContext(ctx,
			"UPDATE users SET state = 'disabled', updated_at_ms = ? WHERE id = ?",
			now.UnixMilli(), targetID)
		if err != nil {
			return fmt.Errorf("disable user: %w", err)
		}
		return revokePendingCapabilities(ctx, connection, targetID, now)
	})
}

func (s *Store) EnableUser(ctx context.Context, actorID, targetID string, now time.Time) error {
	actorID, targetID, now, err := validateUserMutation(actorID, targetID, now)
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		if _, _, err := loadRoleAndState(ctx, connection, targetID); err != nil {
			return err
		}
		_, err := connection.ExecContext(ctx,
			"UPDATE users SET state = 'active', updated_at_ms = ? WHERE id = ?",
			now.UnixMilli(), targetID)
		return err
	})
}

func (s *Store) SetUserRole(ctx context.Context, actorID, targetID string, role Role, now time.Time) error {
	if err := validateRole(role); err != nil {
		return err
	}
	actorID, targetID, now, err := validateUserMutation(actorID, targetID, now)
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(connection *sql.Conn) error {
		if err := requireActiveAdmin(ctx, connection, actorID); err != nil {
			return err
		}
		currentRole, state, err := loadRoleAndState(ctx, connection, targetID)
		if err != nil {
			return err
		}
		if currentRole == role {
			return nil
		}
		if currentRole == RoleAdmin && role != RoleAdmin && state == UserActive {
			if err := requireAnotherActiveAdmin(ctx, connection, targetID); err != nil {
				return err
			}
		}
		_, err = connection.ExecContext(ctx,
			"UPDATE users SET role = ?, updated_at_ms = ? WHERE id = ?",
			string(role), now.UnixMilli(), targetID)
		if err != nil {
			return fmt.Errorf("set user role: %w", err)
		}
		if currentRole == RoleAdmin && role != RoleAdmin {
			return revokePendingCapabilities(ctx, connection, targetID, now)
		}
		return nil
	})
}

func revokePendingCapabilities(ctx context.Context, connection *sql.Conn, userID string, now time.Time) error {
	if _, err := connection.ExecContext(ctx, `UPDATE invitations
		SET state = 'revoked',
			resolved_at_ms = CASE WHEN created_at_ms > ? THEN created_at_ms ELSE ? END
		WHERE created_by = ? AND state = 'pending'`,
		now.UnixMilli(), now.UnixMilli(), userID); err != nil {
		return fmt.Errorf("revoke issued invitations: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `UPDATE password_reset_tickets
		SET state = 'revoked',
			resolved_at_ms = CASE WHEN created_at_ms > ? THEN created_at_ms ELSE ? END
		WHERE (created_by = ? OR user_id = ?) AND state = 'pending'`,
		now.UnixMilli(), now.UnixMilli(), userID, userID); err != nil {
		return fmt.Errorf("revoke issued password reset tickets: %w", err)
	}
	return nil
}

func validateUserMutation(actorID, targetID string, now time.Time) (string, string, time.Time, error) {
	actorID, err := normalizeID(actorID)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("actor id: %w", err)
	}
	targetID, err = normalizeID(targetID)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("target id: %w", err)
	}
	now, err = validateOperationTime(now)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return actorID, targetID, now, nil
}

func requireActiveAdmin(ctx context.Context, connection *sql.Conn, userID string) error {
	role, state, err := loadRoleAndState(ctx, connection, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		return err
	}
	if role != RoleAdmin || state != UserActive {
		return ErrForbidden
	}
	return nil
}

func loadRoleAndState(ctx context.Context, connection *sql.Conn, userID string) (Role, UserState, error) {
	var role Role
	var state UserState
	err := connection.QueryRowContext(ctx,
		"SELECT role, state FROM users WHERE id = ?", userID).Scan(&role, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("load user authorization state: %w", err)
	}
	return role, state, nil
}

func requireAnotherActiveAdmin(ctx context.Context, connection *sql.Conn, excludedID string) error {
	var count int
	if err := connection.QueryRowContext(ctx,
		`SELECT count(*) FROM users
		 WHERE role = 'admin' AND state = 'active' AND id <> ?`, excludedID).Scan(&count); err != nil {
		return fmt.Errorf("count active administrators: %w", err)
	}
	if count == 0 {
		return ErrLastActiveAdmin
	}
	return nil
}

type preparedNewUser struct {
	id           string
	email        string
	display      string
	vaultID      string
	passwordJSON string
	recovery     *preparedRecoveryEnvelope
}

type preparedRecoveryEnvelope struct {
	id           string
	envelopeJSON string
}

func prepareNewUser(input NewUserInput, now time.Time) (preparedNewUser, error) {
	// UserID and VaultID are authenticated data in keyenvelope.Envelope. They
	// must be chosen before wrapping the DEK; silently generating either here
	// would make a valid envelope permanently impossible to unwrap.
	id, err := normalizeID(input.ID)
	if err != nil {
		return preparedNewUser{}, fmt.Errorf("user id: %w", err)
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return preparedNewUser{}, err
	}
	display, err := normalizeDisplayName(input.DisplayName)
	if err != nil {
		return preparedNewUser{}, err
	}
	vaultID, err := normalizeVaultID(input.VaultID)
	if err != nil {
		return preparedNewUser{}, err
	}
	if err := validatePasswordCredential(input.PasswordCredential); err != nil {
		return preparedNewUser{}, err
	}
	passwordJSON, err := encodeKeyEnvelope(input.PasswordCredential.Envelope, keyenvelope.KindPassword)
	if err != nil {
		return preparedNewUser{}, err
	}
	result := preparedNewUser{
		id:           id,
		email:        email,
		display:      display,
		vaultID:      vaultID,
		passwordJSON: passwordJSON,
	}
	if input.RecoveryEnvelope == nil {
		return preparedNewUser{}, errors.New("an active recovery envelope is required")
	}
	if err := validateRecoveryEnvelope(*input.RecoveryEnvelope); err != nil {
		return preparedNewUser{}, err
	}
	recoveryID, err := idOrGenerate(input.RecoveryEnvelope.ID)
	if err != nil {
		return preparedNewUser{}, fmt.Errorf("recovery envelope id: %w", err)
	}
	recoveryJSON, err := encodeKeyEnvelope(input.RecoveryEnvelope.Envelope, keyenvelope.KindRecovery)
	if err != nil {
		return preparedNewUser{}, err
	}
	result.recovery = &preparedRecoveryEnvelope{
		id:           recoveryID,
		envelopeJSON: recoveryJSON,
	}
	return result, nil
}

func (p preparedNewUser) summary(role Role, now time.Time) UserSummary {
	return UserSummary{
		ID:          p.id,
		Email:       p.email,
		DisplayName: p.display,
		Role:        role,
		State:       UserActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func insertPreparedUser(ctx context.Context, connection *sql.Conn, user preparedNewUser, role Role, now time.Time) error {
	if err := validateRole(role); err != nil {
		return err
	}
	timestamp := now.UnixMilli()
	_, err := connection.ExecContext(ctx, `INSERT INTO users(
		id, email, display_name, role, state, vault_id, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, ?, 'active', ?, ?, ?)`,
		user.id, user.email, user.display, string(role), user.vaultID, timestamp, timestamp)
	if err != nil {
		if isSQLiteConstraint(err) {
			return fmt.Errorf("%w: user id, email, or vault id already exists", ErrConflict)
		}
		return fmt.Errorf("insert control user: %w", err)
	}
	_, err = connection.ExecContext(ctx, `INSERT INTO password_credentials(
		user_id, envelope_json, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, ?)`,
		user.id,
		user.passwordJSON,
		timestamp,
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("insert password credential: %w", err)
	}
	if user.recovery != nil {
		_, err = connection.ExecContext(ctx, `INSERT INTO recovery_envelopes(
			id, user_id, envelope_json, state, created_at_ms
		) VALUES (?, ?, ?, 'active', ?)`,
			user.recovery.id,
			user.id,
			user.recovery.envelopeJSON,
			timestamp,
		)
		if err != nil {
			return fmt.Errorf("insert recovery envelope: %w", err)
		}
	}
	return nil
}
