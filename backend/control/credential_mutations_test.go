package control

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPurposeSpecificExpiryLimits(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)

	if _, err := store.CreateInvitation(context.Background(), admin.ID, CreateInvitationInput{
		Email:     "seven-days@example.com",
		Role:      RoleUser,
		TokenHash: testTokenHash(t, 80),
		ExpiresAt: testNow.Add(MaxInvitationLifetime),
	}, testNow); err != nil {
		t.Fatalf("exact maximum invitation lifetime: %v", err)
	}
	if _, err := store.CreateInvitation(context.Background(), admin.ID, CreateInvitationInput{
		Email:     "too-long@example.com",
		Role:      RoleUser,
		TokenHash: testTokenHash(t, 81),
		ExpiresAt: testNow.Add(MaxInvitationLifetime + time.Millisecond),
	}, testNow); err == nil {
		t.Fatal("invitation lifetime above seven days was accepted")
	}

	resetHash := testTokenHash(t, 82)
	if _, err := store.CreatePasswordResetTicket(
		context.Background(),
		admin.ID,
		admin.ID,
		CreatePasswordResetTicketInput{
			TokenHash: resetHash,
			ExpiresAt: testNow.Add(MaxPasswordResetLifetime),
		},
		testNow,
	); err != nil {
		t.Fatalf("exact maximum reset lifetime: %v", err)
	}
	if _, err := store.CreatePasswordResetTicket(
		context.Background(),
		admin.ID,
		admin.ID,
		CreatePasswordResetTicketInput{
			TokenHash: testTokenHash(t, 83),
			ExpiresAt: testNow.Add(MaxPasswordResetLifetime + time.Millisecond),
		},
		testNow,
	); err == nil {
		t.Fatal("password reset lifetime above one hour was accepted")
	}
	ticket, err := store.GetPasswordResetTicketByTokenHash(context.Background(), resetHash)
	if err != nil || ticket.State != PasswordResetPending {
		t.Fatalf("valid reset ticket changed after rejected creation: %#v, %v", ticket, err)
	}
}

func TestRecordSuccessfulLoginRequiresActiveUserAndIsMonotonic(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	adminCredential, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest := testNow.Add(3 * time.Minute)
	if err := store.RecordSuccessfulLogin(context.Background(), admin.ID, adminCredential, latest); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccessfulLogin(context.Background(), admin.ID, adminCredential, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetUser(context.Background(), admin.ID)
	if err != nil || stored.LastLoginAt == nil || !stored.LastLoginAt.Equal(latest) {
		t.Fatalf("last login = %#v, err=%v; want %s", stored.LastLoginAt, err, latest)
	}

	member := inviteAndAccept(t, store, admin.ID, testMemberID, "disabled-login@example.com", RoleUser, 84)
	memberCredential, err := store.GetPasswordCredential(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DisableUser(context.Background(), admin.ID, member.ID, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccessfulLogin(context.Background(), member.ID, memberCredential, testNow.Add(5*time.Minute)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled user login record error = %v", err)
	}
	member, err = store.GetUser(context.Background(), member.ID)
	if err != nil || member.LastLoginAt != nil {
		t.Fatalf("disabled user's last login changed: %#v, %v", member, err)
	}
}

func TestRecordSuccessfulLoginRequiresExactCredentialEnvelopeAndRevision(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	verifiedCredential, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		expected PasswordCredential
	}{
		{
			name: "envelope",
			expected: func() PasswordCredential {
				changed := verifiedCredential
				changed.Envelope = testCredential(120).Envelope
				return changed
			}(),
		},
		{
			name: "updated-at revision",
			expected: func() PasswordCredential {
				changed := verifiedCredential
				changed.UpdatedAt = changed.UpdatedAt.Add(time.Millisecond)
				return changed
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.RecordSuccessfulLogin(
				context.Background(), admin.ID, test.expected, testNow.Add(time.Minute),
			); !errors.Is(err, ErrCredentialConflict) {
				t.Fatalf("credential CAS error = %v, want ErrCredentialConflict", err)
			}
		})
	}
	stored, err := store.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastLoginAt != nil {
		t.Fatalf("credential CAS failure changed last-login time: %v", stored.LastLoginAt)
	}
}

func TestRecordSuccessfulLoginRejectsCredentialChangedDuringPasswordVerification(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	verifiedCredential, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Model a slow password KDF: the login has already loaded and verified the
	// old envelope when a password change commits before the login commit step.
	passwordChanged := make(chan struct{})
	changeErr := make(chan error, 1)
	go func() {
		changeErr <- store.ReplacePasswordCredential(
			context.Background(),
			admin.ID,
			verifiedCredential,
			testCredential(121),
			testNow.Add(time.Minute),
		)
		close(passwordChanged)
	}()
	<-passwordChanged
	if err := <-changeErr; err != nil {
		t.Fatal(err)
	}

	if err := store.RecordSuccessfulLogin(
		context.Background(), admin.ID, verifiedCredential, testNow.Add(2*time.Minute),
	); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("stale credential login commit error = %v, want ErrCredentialConflict", err)
	}
	stored, err := store.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastLoginAt != nil {
		t.Fatalf("stale credential login changed last-login time: %v", stored.LastLoginAt)
	}
}

func TestRecordSuccessfulLoginRejectsCredentialResetDuringPasswordVerification(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	member := inviteAndAccept(t, store, admin.ID, testMemberID, "login-reset-race@example.com", RoleUser, 122)
	verifiedCredential, err := store.GetPasswordCredential(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedRecovery, err := store.GetActiveRecoveryEnvelope(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := testTokenHash(t, 123)
	if _, err := store.CreatePasswordResetTicket(
		context.Background(),
		admin.ID,
		member.ID,
		CreatePasswordResetTicketInput{
			TokenHash: tokenHash,
			ExpiresAt: testNow.Add(30 * time.Minute),
		},
		testNow.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	resetDone := make(chan struct{})
	resetErr := make(chan error, 1)
	go func() {
		replacementRecovery := *testRecovery(124)
		replacementRecovery.ID = "recovery_login_race_12345"
		_, completeErr := store.CompletePasswordReset(
			context.Background(),
			CompletePasswordResetInput{
				TokenHash:                tokenHash,
				ExpectedRecoveryEnvelope: expectedRecovery,
				PasswordCredential:       testCredential(124),
				RecoveryEnvelope:         replacementRecovery,
			},
			testNow.Add(2*time.Minute),
		)
		resetErr <- completeErr
		close(resetDone)
	}()
	<-resetDone
	if err := <-resetErr; err != nil {
		t.Fatal(err)
	}

	if err := store.RecordSuccessfulLogin(
		context.Background(), member.ID, verifiedCredential, testNow.Add(3*time.Minute),
	); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("pre-reset credential login commit error = %v, want ErrCredentialConflict", err)
	}
	stored, err := store.GetUser(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastLoginAt != nil {
		t.Fatalf("pre-reset credential login changed last-login time: %v", stored.LastLoginAt)
	}
}

func TestReplacePasswordCredentialUsesEnvelopeAndRevisionCAS(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	expected, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePasswordCredential(
		context.Background(), admin.ID, expected, testCredential(84), expected.UpdatedAt.Add(500*time.Microsecond),
	); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("same-millisecond credential revision error = %v", err)
	}
	replacement := testCredential(85)
	changedAt := testNow.Add(time.Minute)
	if err := store.ReplacePasswordCredential(
		context.Background(), admin.ID, expected, replacement, changedAt,
	); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Envelope, replacement.Envelope) || !stored.UpdatedAt.Equal(changedAt) {
		t.Fatalf("stored replacement = %#v", stored)
	}
	if err := store.ReplacePasswordCredential(
		context.Background(), admin.ID, expected, testCredential(86), testNow.Add(2*time.Minute),
	); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("stale credential CAS error = %v", err)
	}
	afterConflict, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil || !reflect.DeepEqual(afterConflict.Envelope, replacement.Envelope) {
		t.Fatalf("CAS conflict changed credential: %#v, %v", afterConflict, err)
	}
}

func TestRotateRecoveryEnvelopeIsAtomicCAS(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	expected, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := *testRecovery(87)
	replacement.ID = "recovery_123456789012345"
	changedAt := testNow.Add(time.Minute)
	rotated, err := store.RotateRecoveryEnvelope(
		context.Background(), admin.ID, expected, replacement, changedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != replacement.ID || rotated.State != RecoveryEnvelopeActive ||
		!reflect.DeepEqual(rotated.Envelope, replacement.Envelope) {
		t.Fatalf("rotated envelope = %#v", rotated)
	}
	var oldState RecoveryEnvelopeState
	if err := store.db.QueryRowContext(context.Background(),
		"SELECT state FROM recovery_envelopes WHERE id = ?", expected.ID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if oldState != RecoveryEnvelopeRevoked {
		t.Fatalf("old recovery state = %s", oldState)
	}
	if _, err := store.RotateRecoveryEnvelope(
		context.Background(), admin.ID, expected, *testRecovery(88), testNow.Add(2*time.Minute),
	); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("stale recovery CAS error = %v", err)
	}
	active, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil || active.ID != replacement.ID {
		t.Fatalf("active recovery after conflict = %#v, %v", active, err)
	}
}

func TestRotateRecoveryEnvelopeRejectsIdenticalEnvelope(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	expected, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := RecoveryEnvelopeInput{
		ID:       "recovery_same_1234567890",
		Envelope: expected.Envelope,
	}
	if _, err := store.RotateRecoveryEnvelope(
		context.Background(), admin.ID, expected, replacement, testNow.Add(time.Minute),
	); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("identical recovery envelope error = %v", err)
	}
	active, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil || active.ID != expected.ID || !reflect.DeepEqual(active.Envelope, expected.Envelope) {
		t.Fatalf("rejected recovery rotation changed state: %#v, %v", active, err)
	}
}

func TestDisabledUserCannotReceiveResetAndRepeatedDisableRevokesPendingTicket(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	member := inviteAndAccept(t, store, admin.ID, testMemberID, "disabled-reset@example.com", RoleUser, 97)
	disabledAt := testNow.Add(2 * time.Minute)
	if err := store.DisableUser(context.Background(), admin.ID, member.ID, disabledAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePasswordResetTicket(
		context.Background(), admin.ID, member.ID,
		CreatePasswordResetTicketInput{
			TokenHash: testTokenHash(t, 98),
			ExpiresAt: disabledAt.Add(30 * time.Minute),
		}, disabledAt.Add(time.Minute),
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled user reset ticket error = %v", err)
	}

	legacyHash := testTokenHash(t, 99)
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO password_reset_tickets(
		id, user_id, token_hash, state, created_by, created_at_ms, expires_at_ms
	) VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		"reset_legacy_1234567890", member.ID, legacyHash, admin.ID,
		disabledAt.Add(time.Minute).UnixMilli(), disabledAt.Add(30*time.Minute).UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableUser(context.Background(), admin.ID, member.ID, disabledAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ticket, err := store.GetPasswordResetTicketByTokenHash(context.Background(), legacyHash)
	if err != nil || ticket.State != PasswordResetRevoked {
		t.Fatalf("pending ticket survived repeated disable: %#v, %v", ticket, err)
	}
}

func TestWithImmediateRollsBackAfterPanic(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	before, err := store.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	panicked := false
	func() {
		defer func() {
			panicked = recover() != nil
		}()
		_ = store.withImmediate(context.Background(), func(connection *sql.Conn) error {
			if _, err := connection.ExecContext(context.Background(),
				"UPDATE users SET display_name = ? WHERE id = ?", "must-roll-back", admin.ID); err != nil {
				return err
			}
			panic("test panic")
		})
	}()
	if !panicked {
		t.Fatal("withImmediate swallowed the operation panic")
	}
	after, err := store.GetUser(context.Background(), admin.ID)
	if err != nil || after.DisplayName != before.DisplayName {
		t.Fatalf("panic transaction was not rolled back: %#v, %v", after, err)
	}
	credential, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccessfulLogin(context.Background(), admin.ID, credential, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("transaction after panic remained locked: %v", err)
	}
}

func TestCompletePasswordResetCommitsAllStateAndReturnsSafeDTO(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	member := inviteAndAccept(t, store, admin.ID, testMemberID, "complete-reset@example.com", RoleUser, 89)
	expectedPassword, err := store.GetPasswordCredential(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedRecovery, err := store.GetActiveRecoveryEnvelope(context.Background(), member.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := testTokenHash(t, 90)
	ticket, err := store.CreatePasswordResetTicket(
		context.Background(), admin.ID, member.ID,
		CreatePasswordResetTicketInput{
			TokenHash: tokenHash,
			ExpiresAt: testNow.Add(40 * time.Minute),
		},
		testNow.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	siblingHash := testTokenHash(t, 91)
	siblingID := "reset_sibling_123456789"
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO password_reset_tickets(
		id, user_id, token_hash, state, created_by, created_at_ms, expires_at_ms
	) VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		siblingID,
		member.ID,
		siblingHash,
		admin.ID,
		testNow.Add(3*time.Minute).UnixMilli(),
		testNow.Add(45*time.Minute).UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}

	wrongExpected := expectedRecovery
	wrongExpected.ID = "recovery_wrong_12345678"
	if _, err := store.CompletePasswordReset(context.Background(), CompletePasswordResetInput{
		TokenHash:                tokenHash,
		ExpectedRecoveryEnvelope: wrongExpected,
		PasswordCredential:       testCredential(92),
		RecoveryEnvelope:         *testRecovery(92),
	}, testNow.Add(4*time.Minute)); !errors.Is(err, ErrRecoveryConflict) {
		t.Fatalf("wrong recovery CAS error = %v", err)
	}
	pending, err := store.GetPasswordResetTicketByTokenHash(context.Background(), tokenHash)
	if err != nil || pending.State != PasswordResetPending {
		t.Fatalf("failed reset changed ticket: %#v, %v", pending, err)
	}
	unchangedPassword, err := store.GetPasswordCredential(context.Background(), member.ID)
	if err != nil || !reflect.DeepEqual(unchangedPassword.Envelope, expectedPassword.Envelope) {
		t.Fatalf("failed reset changed password: %#v, %v", unchangedPassword, err)
	}

	replacementRecovery := *testRecovery(93)
	replacementRecovery.ID = "recovery_reset_1234567890"
	replacementPassword := testCredential(93)
	completed, err := store.CompletePasswordReset(context.Background(), CompletePasswordResetInput{
		TokenHash:                tokenHash,
		ExpectedRecoveryEnvelope: expectedRecovery,
		PasswordCredential:       replacementPassword,
		RecoveryEnvelope:         replacementRecovery,
	}, testNow.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if completed.ID != ticket.ID || completed.State != PasswordResetConsumed || completed.ResolvedAt == nil {
		t.Fatalf("completed reset ticket = %#v", completed)
	}
	forbiddenFragments := []string{"token", "hash", "password", "envelope", "salt", "verifier"}
	typeInfo := reflect.TypeOf(completed)
	for index := 0; index < typeInfo.NumField(); index++ {
		field := typeInfo.Field(index).Name
		for _, fragment := range forbiddenFragments {
			if containsFold(field, fragment) {
				t.Fatalf("PasswordResetTicket exposes secret-bearing field %q", field)
			}
		}
	}
	storedPassword, err := store.GetPasswordCredential(context.Background(), member.ID)
	if err != nil || !reflect.DeepEqual(storedPassword.Envelope, replacementPassword.Envelope) {
		t.Fatalf("completed reset password = %#v, %v", storedPassword, err)
	}
	storedRecovery, err := store.GetActiveRecoveryEnvelope(context.Background(), member.ID)
	if err != nil || storedRecovery.ID != replacementRecovery.ID ||
		!reflect.DeepEqual(storedRecovery.Envelope, replacementRecovery.Envelope) {
		t.Fatalf("completed reset recovery = %#v, %v", storedRecovery, err)
	}
	var siblingState PasswordResetState
	if err := store.db.QueryRowContext(context.Background(),
		"SELECT state FROM password_reset_tickets WHERE id = ?", siblingID).Scan(&siblingState); err != nil {
		t.Fatal(err)
	}
	if siblingState != PasswordResetRevoked {
		t.Fatalf("sibling reset ticket state = %s", siblingState)
	}
	if _, err := store.CompletePasswordReset(context.Background(), CompletePasswordResetInput{
		TokenHash:                tokenHash,
		ExpectedRecoveryEnvelope: storedRecovery,
		PasswordCredential:       testCredential(94),
		RecoveryEnvelope:         *testRecovery(94),
	}, testNow.Add(6*time.Minute)); !errors.Is(err, ErrResetTicketInactive) {
		t.Fatalf("reused completed reset error = %v", err)
	}
}

func TestCompleteExpiredPasswordResetDoesNotChangeEnvelopes(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	expectedPassword, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedRecovery, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := testTokenHash(t, 95)
	if _, err := store.CreatePasswordResetTicket(
		context.Background(), admin.ID, admin.ID,
		CreatePasswordResetTicketInput{
			TokenHash: tokenHash,
			ExpiresAt: testNow.Add(10 * time.Minute),
		},
		testNow,
	); err != nil {
		t.Fatal(err)
	}
	replacementRecovery := *testRecovery(96)
	replacementRecovery.ID = "recovery_expired_1234567"
	if _, err := store.CompletePasswordReset(context.Background(), CompletePasswordResetInput{
		TokenHash:                tokenHash,
		ExpectedRecoveryEnvelope: expectedRecovery,
		PasswordCredential:       testCredential(96),
		RecoveryEnvelope:         replacementRecovery,
	}, testNow.Add(11*time.Minute)); !errors.Is(err, ErrResetTicketExpired) {
		t.Fatalf("expired reset completion error = %v", err)
	}
	password, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil || !reflect.DeepEqual(password.Envelope, expectedPassword.Envelope) {
		t.Fatalf("expired reset changed password: %#v, %v", password, err)
	}
	recovery, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil || recovery.ID != expectedRecovery.ID {
		t.Fatalf("expired reset changed recovery: %#v, %v", recovery, err)
	}
	ticket, err := store.GetPasswordResetTicketByTokenHash(context.Background(), tokenHash)
	if err != nil || ticket.State != PasswordResetExpired {
		t.Fatalf("expired reset ticket = %#v, %v", ticket, err)
	}
}

func containsFold(value, fragment string) bool {
	for start := 0; start+len(fragment) <= len(value); start++ {
		if equalFoldASCII(value[start:start+len(fragment)], fragment) {
			return true
		}
	}
	return false
}

func equalFoldASCII(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		l, r := left[index], right[index]
		if l >= 'A' && l <= 'Z' {
			l += 'a' - 'A'
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if l != r {
			return false
		}
	}
	return true
}
