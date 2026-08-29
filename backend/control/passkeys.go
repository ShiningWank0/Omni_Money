package control

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/keyenvelope"
)

const maxPasskeysPerUser = 10

func (s *Store) CreatePasskeyCredential(ctx context.Context, input PasskeyCredentialInput, now time.Time) (PasskeyCredential, error) {
	prepared, credentialJSON, envelopeJSON, err := preparePasskeyCredential(input)
	if err != nil {
		return PasskeyCredential{}, err
	}
	db, err := s.database()
	if err != nil {
		return PasskeyCredential{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PasskeyCredential{}, fmt.Errorf("begin passkey creation: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM passkey_credentials WHERE user_id = ?", prepared.UserID).Scan(&count); err != nil {
		return PasskeyCredential{}, fmt.Errorf("count passkeys: %w", err)
	}
	if count >= maxPasskeysPerUser {
		return PasskeyCredential{}, fmt.Errorf("%w: no more than %d passkeys may be registered", ErrConflict, maxPasskeysPerUser)
	}
	timestamp := now.UTC().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO passkey_credentials(
		credential_id, user_id, name, credential_json, prf_salt,
		vault_envelope_json, revision, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		prepared.ID, prepared.UserID, prepared.Name, credentialJSON, prepared.PRFSalt,
		envelopeJSON, timestamp, timestamp)
	if err != nil {
		if isSQLiteConstraint(err) {
			return PasskeyCredential{}, fmt.Errorf("%w: passkey credential already exists", ErrConflict)
		}
		return PasskeyCredential{}, fmt.Errorf("insert passkey credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PasskeyCredential{}, fmt.Errorf("commit passkey credential: %w", err)
	}
	prepared.Revision = 1
	prepared.CreatedAt = now.UTC()
	prepared.UpdatedAt = now.UTC()
	return prepared, nil
}

func (s *Store) ListPasskeyCredentials(ctx context.Context, userID string) ([]PasskeyCredential, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT credential_id, user_id, name, credential_json,
		prf_salt, vault_envelope_json, revision, created_at_ms, updated_at_ms, last_used_at_ms
		FROM passkey_credentials WHERE user_id = ? ORDER BY created_at_ms, credential_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	results := make([]PasskeyCredential, 0)
	for rows.Next() {
		credential, err := scanPasskeyCredential(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	return results, nil
}

func (s *Store) GetPasskeyCredential(ctx context.Context, userID string, credentialID []byte) (PasskeyCredential, error) {
	userID, err := normalizeID(userID)
	if err != nil {
		return PasskeyCredential{}, err
	}
	if err := validatePasskeyCredentialID(credentialID); err != nil {
		return PasskeyCredential{}, err
	}
	db, err := s.database()
	if err != nil {
		return PasskeyCredential{}, err
	}
	result, err := scanPasskeyCredential(db.QueryRowContext(ctx, `SELECT credential_id, user_id, name, credential_json,
		prf_salt, vault_envelope_json, revision, created_at_ms, updated_at_ms, last_used_at_ms
		FROM passkey_credentials WHERE user_id = ? AND credential_id = ?`, userID, credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return PasskeyCredential{}, ErrNotFound
	}
	return result, err
}

func (s *Store) RecordSuccessfulPasskeyUse(
	ctx context.Context,
	expected PasskeyCredential,
	updated webauthn.Credential,
	now time.Time,
	recordLogin bool,
) error {
	if expected.Revision < 1 || !bytes.Equal(expected.ID, updated.ID) {
		return ErrCredentialConflict
	}
	encoded, err := json.Marshal(updated)
	if err != nil || len(encoded) < 2 || len(encoded) > 1<<20 {
		return fmt.Errorf("encode updated passkey credential: %w", err)
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin passkey login commit: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE passkey_credentials
		SET credential_json = ?, revision = revision + 1, updated_at_ms = ?, last_used_at_ms = ?
		WHERE user_id = ? AND credential_id = ? AND revision = ?`,
		string(encoded), now.UTC().UnixMilli(), now.UTC().UnixMilli(), expected.UserID, expected.ID, expected.Revision)
	if err != nil {
		return fmt.Errorf("record passkey login: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("verify passkey login update: %w", err)
	}
	if affected != 1 {
		return ErrCredentialConflict
	}
	if recordLogin {
		userResult, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at_ms = ?, updated_at_ms =
			CASE WHEN updated_at_ms < ? THEN ? ELSE updated_at_ms END
			WHERE id = ? AND state = 'active'`, now.UTC().UnixMilli(), now.UTC().UnixMilli(), now.UTC().UnixMilli(), expected.UserID)
		if err != nil {
			return fmt.Errorf("record passkey user login: %w", err)
		}
		userAffected, err := userResult.RowsAffected()
		if err != nil || userAffected != 1 {
			return ErrCredentialConflict
		}
	} else {
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = ? AND state = 'active'", expected.UserID).Scan(&active); err != nil || active != 1 {
			return ErrCredentialConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit passkey login: %w", err)
	}
	return nil
}

func (s *Store) DeletePasskeyCredential(ctx context.Context, userID string, credentialID []byte) error {
	userID, err := normalizeID(userID)
	if err != nil {
		return err
	}
	if err := validatePasskeyCredentialID(credentialID); err != nil {
		return err
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, "DELETE FROM passkey_credentials WHERE user_id = ? AND credential_id = ?", userID, credentialID)
	if err != nil {
		return fmt.Errorf("delete passkey credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func preparePasskeyCredential(input PasskeyCredentialInput) (PasskeyCredential, string, string, error) {
	userID, err := normalizeID(input.UserID)
	if err != nil {
		return PasskeyCredential{}, "", "", err
	}
	name := strings.TrimSpace(input.Name)
	if len(name) == 0 || len(name) > 120 {
		return PasskeyCredential{}, "", "", errors.New("passkey name must contain between 1 and 120 bytes")
	}
	if err := validatePasskeyCredentialID(input.Credential.ID); err != nil {
		return PasskeyCredential{}, "", "", err
	}
	if len(input.Credential.PublicKey) == 0 {
		return PasskeyCredential{}, "", "", errors.New("passkey public key is required")
	}
	if len(input.PRFSalt) != keyenvelope.PasskeySecretSize {
		return PasskeyCredential{}, "", "", errors.New("passkey PRF salt must be exactly 32 bytes")
	}
	credentialJSON, err := json.Marshal(input.Credential)
	if err != nil || len(credentialJSON) < 2 || len(credentialJSON) > 1<<20 {
		return PasskeyCredential{}, "", "", fmt.Errorf("encode passkey credential: %w", err)
	}
	envelopeJSON, err := encodeKeyEnvelope(input.VaultEnvelope, keyenvelope.KindPasskey)
	if err != nil {
		return PasskeyCredential{}, "", "", err
	}
	result := PasskeyCredential{
		ID: append([]byte(nil), input.Credential.ID...), UserID: userID, Name: name,
		Credential: input.Credential, PRFSalt: append([]byte(nil), input.PRFSalt...), VaultEnvelope: input.VaultEnvelope,
	}
	return result, string(credentialJSON), envelopeJSON, nil
}

type passkeyScanner interface {
	Scan(...any) error
}

func scanPasskeyCredential(scanner passkeyScanner) (PasskeyCredential, error) {
	var result PasskeyCredential
	var credentialJSON, envelopeJSON string
	var createdAt, updatedAt int64
	var lastUsed sql.NullInt64
	if err := scanner.Scan(&result.ID, &result.UserID, &result.Name, &credentialJSON,
		&result.PRFSalt, &envelopeJSON, &result.Revision, &createdAt, &updatedAt, &lastUsed); err != nil {
		return PasskeyCredential{}, err
	}
	if err := json.Unmarshal([]byte(credentialJSON), &result.Credential); err != nil {
		return PasskeyCredential{}, fmt.Errorf("decode passkey credential: %w", err)
	}
	if !bytes.Equal(result.ID, result.Credential.ID) {
		return PasskeyCredential{}, errors.New("stored passkey credential ID mismatch")
	}
	var err error
	result.VaultEnvelope, err = decodeKeyEnvelope(envelopeJSON, keyenvelope.KindPasskey)
	if err != nil {
		return PasskeyCredential{}, fmt.Errorf("decode passkey envelope: %w", err)
	}
	result.ID = append([]byte(nil), result.ID...)
	result.PRFSalt = append([]byte(nil), result.PRFSalt...)
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	if lastUsed.Valid {
		value := time.UnixMilli(lastUsed.Int64).UTC()
		result.LastUsedAt = &value
	}
	return result, nil
}

func validatePasskeyCredentialID(id []byte) error {
	if len(id) < 16 || len(id) > 1024 {
		return errors.New("passkey credential ID must contain between 16 and 1024 bytes")
	}
	return nil
}

func (credential PasskeyCredential) Summary() PasskeySummary {
	return PasskeySummary{
		ID: base64.RawURLEncoding.EncodeToString(credential.ID), Name: credential.Name,
		CreatedAt: credential.CreatedAt, LastUsedAt: credential.LastUsedAt,
	}
}
