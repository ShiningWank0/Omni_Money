package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"omni_money/backend/keyenvelope"
	"omni_money/backend/securedb"
)

const (
	testAdminID  = "admin_1234567890123456"
	testSecondID = "second_12345678901234"
	testMemberID = "member_12345678901234"
)

var testNow = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatalf("secure test directory: %v", err)
	}
	store, err := openStore(
		context.Background(),
		securedb.NewPlainOpener(),
		filepath.Join(directory, "control.db"),
		false,
	)
	if err != nil {
		t.Fatalf("open test control store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close control store: %v", err)
		}
	})
	return store
}

func testCredential(seed byte) PasswordCredentialInput {
	return PasswordCredentialInput{
		Envelope: keyenvelope.Envelope{
			Version:    keyenvelope.CurrentVersion,
			Kind:       keyenvelope.KindPassword,
			KDF:        passwordEnvelopeKDF,
			Profile:    keyenvelope.DefaultProfile(),
			Salt:       bytes.Repeat([]byte{seed + 1}, keyenvelope.SaltSize),
			Nonce:      bytes.Repeat([]byte{seed + 2}, 12),
			Ciphertext: bytes.Repeat([]byte{seed + 3}, keyenvelope.DEKSize+16),
			Verifier:   bytes.Repeat([]byte{seed + 4}, keyenvelope.VerifierSize),
		},
	}
}

func testRecovery(seed byte) *RecoveryEnvelopeInput {
	return &RecoveryEnvelopeInput{
		Envelope: keyenvelope.Envelope{
			Version:    keyenvelope.CurrentVersion,
			Kind:       keyenvelope.KindRecovery,
			KDF:        recoveryEnvelopeKDF,
			Salt:       bytes.Repeat([]byte{seed + 5}, keyenvelope.SaltSize),
			Nonce:      bytes.Repeat([]byte{seed + 6}, 12),
			Ciphertext: bytes.Repeat([]byte{seed + 7}, keyenvelope.DEKSize+16),
		},
	}
}

func testNewUser(t *testing.T, id, email string, seed byte) NewUserInput {
	t.Helper()
	return NewUserInput{
		ID:                 id,
		Email:              email,
		DisplayName:        "Test User'); DROP TABLE users; --",
		VaultID:            "vault_" + id,
		PasswordCredential: testCredential(seed),
		RecoveryEnvelope:   testRecovery(seed),
	}
}

func bootstrapTestAdmin(t *testing.T, store *Store) UserSummary {
	t.Helper()
	admin, err := store.BootstrapFirstAdmin(
		context.Background(),
		testNewUser(t, testAdminID, "ADMIN@example.com", 1),
		testNow,
	)
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	return admin
}

func testTokenHash(t *testing.T, seed byte) []byte {
	t.Helper()
	token := bytes.Repeat([]byte{seed}, 32)
	hash, err := HashBearerToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func inviteAndAccept(
	t *testing.T,
	store *Store,
	adminID, userID, email string,
	role Role,
	seed byte,
) UserSummary {
	t.Helper()
	hash := testTokenHash(t, seed)
	if _, err := store.CreateInvitation(context.Background(), adminID, CreateInvitationInput{
		Email:     email,
		Role:      role,
		TokenHash: hash,
		ExpiresAt: testNow.Add(time.Hour),
	}, testNow); err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	user, err := store.AcceptInvitation(
		context.Background(),
		hash,
		testNewUser(t, userID, email, seed),
		testNow.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	return user
}

func TestOpenRequiresEncryptionAndPrivateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist.db")
	if _, err := Open(context.Background(), securedb.NewPlainOpener(), path); !errors.Is(err, ErrEncryptionRequired) {
		t.Fatalf("Open error = %v, want ErrEncryptionRequired", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plaintext Open modified path: %v", err)
	}

	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(context.Background(), securedb.NewPlainOpener(), filepath.Join(directory, "control.db"), false); err == nil || !strings.Contains(err.Error(), "only by its owner") {
		t.Fatalf("openStore accepted public directory: %v", err)
	}
	var key securedb.RawKey
	for index := range key {
		key[index] = 0x43
	}
	encryptedOpener := securedb.NewEncryptedOpener(key)
	if _, err := Open(context.Background(), encryptedOpener, filepath.Join(directory, "encrypted.db")); err == nil {
		t.Fatal("Open accepted public directory with encrypted opener")
	}
	if encryptedOpener.Encrypted() {
		t.Fatal("failed Open retained its owned SQLCipher opener key")
	}
}

func TestBootstrapAndListUsersDoNotExposeSecrets(t *testing.T) {
	store := openTestStore(t)
	if bootstrapped, err := store.IsBootstrapped(context.Background()); err != nil || bootstrapped {
		t.Fatalf("empty bootstrap state = %v, %v", bootstrapped, err)
	}
	admin := bootstrapTestAdmin(t, store)
	if bootstrapped, err := store.IsBootstrapped(context.Background()); err != nil || !bootstrapped {
		t.Fatalf("populated bootstrap state = %v, %v", bootstrapped, err)
	}
	if admin.Email != "admin@example.com" || admin.Role != RoleAdmin || admin.State != UserActive {
		t.Fatalf("unexpected admin: %#v", admin)
	}
	users, err := store.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].DisplayName != "Test User'); DROP TABLE users; --" {
		t.Fatalf("unexpected users: %#v", users)
	}
	byEmail, err := store.GetUserByEmail(context.Background(), " ADMIN@EXAMPLE.COM ")
	if err != nil || byEmail.ID != admin.ID {
		t.Fatalf("email lookup = %#v, %v", byEmail, err)
	}

	for _, forbidden := range []string{"password", "verifier", "salt", "envelope", "vault", "financial", "balance"} {
		typeInfo := reflect.TypeOf(UserSummary{})
		for index := 0; index < typeInfo.NumField(); index++ {
			if strings.Contains(strings.ToLower(typeInfo.Field(index).Name), forbidden) {
				t.Fatalf("UserSummary exposes forbidden field %q", typeInfo.Field(index).Name)
			}
		}
	}
	credential, err := store.GetPasswordCredential(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(credential.Envelope, testCredential(1).Envelope) {
		t.Fatal("password key envelope did not round-trip exactly")
	}
	recovery, err := store.GetActiveRecoveryEnvelope(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovery.Envelope, testRecovery(1).Envelope) {
		t.Fatal("recovery key envelope did not round-trip exactly")
	}
	if vaultID, err := store.LookupVaultID(context.Background(), admin.ID); err != nil || vaultID != "vault_"+admin.ID {
		t.Fatalf("lookup private vault id = %q, %v", vaultID, err)
	}
}

func TestKeyEnvelopeMetadataAndCiphertextRoundTrip(t *testing.T) {
	store := openTestStore(t)
	userID := testAdminID
	vaultID := "vault_" + userID
	contextBinding := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	dek := bytes.Repeat([]byte{0x91}, keyenvelope.DEKSize)
	password := []byte("correct horse battery staple")
	recoverySecret := bytes.Repeat([]byte{0xa7}, keyenvelope.RecoverySecretSize)
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, password, contextBinding)
	if err != nil {
		t.Fatal(err)
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, recoverySecret, contextBinding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BootstrapFirstAdmin(context.Background(), BootstrapAdminInput{
		ID:                 userID,
		Email:              "envelope@example.com",
		DisplayName:        "Envelope User",
		VaultID:            vaultID,
		PasswordCredential: PasswordCredentialInput{Envelope: *passwordEnvelope},
		RecoveryEnvelope:   &RecoveryEnvelopeInput{Envelope: *recoveryEnvelope},
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	storedPassword, err := store.GetPasswordCredential(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedPassword.Envelope, *passwordEnvelope) {
		t.Fatal("password envelope metadata or ciphertext changed in storage")
	}
	unwrapped, err := keyenvelope.UnwrapWithPassword(&storedPassword.Envelope, password, contextBinding)
	if err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("unwrap stored password envelope: %v", err)
	}
	clear(unwrapped)
	storedRecovery, err := store.GetActiveRecoveryEnvelope(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedRecovery.Envelope, *recoveryEnvelope) {
		t.Fatal("recovery envelope metadata or ciphertext changed in storage")
	}
	unwrapped, err = keyenvelope.UnwrapWithRecovery(&storedRecovery.Envelope, recoverySecret, contextBinding)
	if err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("unwrap stored recovery envelope: %v", err)
	}
	clear(unwrapped)
}

func TestBootstrapFirstAdminIsTransactional(t *testing.T) {
	store := openTestStore(t)
	inputs := []NewUserInput{
		testNewUser(t, testAdminID, "first@example.com", 10),
		testNewUser(t, testSecondID, "second@example.com", 20),
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(inputs))
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.BootstrapFirstAdmin(context.Background(), input, testNow)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	var successes, already int
	for err := range errorsSeen {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyBootstrapped):
			already++
		default:
			t.Fatalf("unexpected bootstrap error: %v", err)
		}
	}
	if successes != 1 || already != 1 {
		t.Fatalf("bootstrap results: successes=%d already=%d", successes, already)
	}
}

func TestInvitationStoresOnlyHashAndIsSingleUse(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	rawToken := bytes.Repeat([]byte("not-persisted-raw-token-material!"), 2)
	hash, err := HashBearerToken(rawToken)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := store.CreateInvitation(context.Background(), admin.ID, CreateInvitationInput{
		Email:     "member@example.com",
		Role:      RoleUser,
		TokenHash: hash,
		ExpiresAt: testNow.Add(time.Hour),
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if invitation.State != InvitationPending {
		t.Fatalf("invitation state = %s", invitation.State)
	}

	var persisted []byte
	if err := store.db.QueryRowContext(context.Background(),
		"SELECT token_hash FROM invitations WHERE id = ?", invitation.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(persisted, rawToken) || !bytes.Equal(persisted, hash) {
		t.Fatal("invitation did not persist exactly the token hash")
	}
	member, err := store.AcceptInvitation(
		context.Background(),
		hash,
		testNewUser(t, testMemberID, "member@example.com", 30),
		testNow.Add(time.Minute),
	)
	if err != nil || member.Role != RoleUser {
		t.Fatalf("accept invitation: user=%#v err=%v", member, err)
	}
	if _, err := store.AcceptInvitation(
		context.Background(), hash,
		testNewUser(t, "another_1234567890123", "member@example.com", 31),
		testNow.Add(2*time.Minute),
	); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("second acceptance error = %v", err)
	}
	resolved, err := store.GetInvitationByTokenHash(context.Background(), hash)
	if err != nil || resolved.State != InvitationAccepted || resolved.AcceptedBy == nil || *resolved.AcceptedBy != member.ID {
		t.Fatalf("resolved invitation = %#v, err=%v", resolved, err)
	}
}

func TestExpiredInvitationCommitsExpiredState(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	hash := testTokenHash(t, 40)
	if _, err := store.CreateInvitation(context.Background(), admin.ID, CreateInvitationInput{
		Email:     "late@example.com",
		Role:      RoleUser,
		TokenHash: hash,
		ExpiresAt: testNow.Add(time.Minute),
	}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInvitation(
		context.Background(), hash,
		testNewUser(t, testMemberID, "late@example.com", 41),
		testNow.Add(2*time.Minute),
	); !errors.Is(err, ErrInvitationExpired) {
		t.Fatalf("expired acceptance error = %v", err)
	}
	invitation, err := store.GetInvitationByTokenHash(context.Background(), hash)
	if err != nil || invitation.State != InvitationExpired || invitation.ResolvedAt == nil {
		t.Fatalf("expired invitation = %#v, err=%v", invitation, err)
	}
}

func TestAdminStateInvariants(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	if err := store.DisableUser(context.Background(), admin.ID, admin.ID, testNow.Add(time.Minute)); !errors.Is(err, ErrSelfDisable) {
		t.Fatalf("self-disable error = %v", err)
	}
	if err := store.SetUserRole(context.Background(), admin.ID, admin.ID, RoleUser, testNow.Add(time.Minute)); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("last-admin demotion error = %v", err)
	}

	second := inviteAndAccept(t, store, admin.ID, testSecondID, "second-admin@example.com", RoleAdmin, 50)
	issuedHash := testTokenHash(t, 52)
	if _, err := store.CreateInvitation(context.Background(), second.ID, CreateInvitationInput{
		Email:     "issued-before-demotion@example.com",
		Role:      RoleUser,
		TokenHash: issuedHash,
		ExpiresAt: testNow.Add(time.Hour),
	}, testNow.Add(90*time.Second)); err != nil {
		t.Fatalf("second admin create invitation: %v", err)
	}
	if err := store.SetUserRole(context.Background(), admin.ID, second.ID, RoleUser, testNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("demote with another admin: %v", err)
	}
	if _, err := store.AcceptInvitation(
		context.Background(),
		issuedHash,
		testNewUser(t, "issued_12345678901234", "issued-before-demotion@example.com", 53),
		testNow.Add(150*time.Second),
	); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("demoted admin's outstanding invitation error = %v", err)
	}
	if err := store.DisableUser(context.Background(), admin.ID, second.ID, testNow.Add(3*time.Minute)); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	updated, err := store.GetUser(context.Background(), second.ID)
	if err != nil || updated.State != UserDisabled || updated.Role != RoleUser {
		t.Fatalf("updated user = %#v, err=%v", updated, err)
	}
	if _, err := store.CreateInvitation(context.Background(), second.ID, CreateInvitationInput{
		Email:     "forbidden@example.com",
		Role:      RoleUser,
		TokenHash: testTokenHash(t, 51),
		ExpiresAt: testNow.Add(time.Hour),
	}, testNow.Add(4*time.Minute)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled member invitation error = %v", err)
	}
}

func TestAdminMutationRevokesCapabilitiesCreatedAfterMutationTime(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Store, string, string, time.Time) error
		assert func(*testing.T, UserSummary)
	}{
		{
			name: "demotion",
			mutate: func(store *Store, actorID, targetID string, now time.Time) error {
				return store.SetUserRole(context.Background(), actorID, targetID, RoleUser, now)
			},
			assert: func(t *testing.T, user UserSummary) {
				t.Helper()
				if user.Role != RoleUser || user.State != UserActive {
					t.Fatalf("demoted user = %#v", user)
				}
			},
		},
		{
			name: "disable",
			mutate: func(store *Store, actorID, targetID string, now time.Time) error {
				return store.DisableUser(context.Background(), actorID, targetID, now)
			},
			assert: func(t *testing.T, user UserSummary) {
				t.Helper()
				if user.Role != RoleAdmin || user.State != UserDisabled {
					t.Fatalf("disabled user = %#v", user)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			admin := bootstrapTestAdmin(t, store)
			target := inviteAndAccept(t, store, admin.ID, testSecondID, "future-capability-admin@example.com", RoleAdmin, 54)
			createdAt := testNow.Add(20 * time.Minute)
			mutationTime := testNow.Add(10 * time.Minute)

			invitationHash := testTokenHash(t, 55)
			if _, err := store.CreateInvitation(context.Background(), target.ID, CreateInvitationInput{
				Email:     "future-capability-member@example.com",
				Role:      RoleUser,
				TokenHash: invitationHash,
				ExpiresAt: createdAt.Add(time.Hour),
			}, createdAt); err != nil {
				t.Fatalf("create future invitation: %v", err)
			}

			resetHash := testTokenHash(t, 56)
			if _, err := store.CreatePasswordResetTicket(
				context.Background(),
				target.ID,
				target.ID,
				CreatePasswordResetTicketInput{
					TokenHash: resetHash,
					ExpiresAt: createdAt.Add(30 * time.Minute),
				},
				createdAt,
			); err != nil {
				t.Fatalf("create future password reset: %v", err)
			}

			if err := test.mutate(store, admin.ID, target.ID, mutationTime); err != nil {
				t.Fatalf("%s admin with future capabilities: %v", test.name, err)
			}
			updated, err := store.GetUser(context.Background(), target.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.assert(t, updated)

			invitation, err := store.GetInvitationByTokenHash(context.Background(), invitationHash)
			if err != nil || invitation.State != InvitationRevoked || invitation.ResolvedAt == nil || !invitation.ResolvedAt.Equal(createdAt) {
				t.Fatalf("future invitation after %s = %#v, %v", test.name, invitation, err)
			}
			reset, err := store.GetPasswordResetTicketByTokenHash(context.Background(), resetHash)
			if err != nil || reset.State != PasswordResetRevoked || reset.ResolvedAt == nil || !reset.ResolvedAt.Equal(createdAt) {
				t.Fatalf("future password reset after %s = %#v, %v", test.name, reset, err)
			}
		})
	}
}

func TestPasswordResetTicketIsHashedAndSuperseded(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	member := inviteAndAccept(t, store, admin.ID, testMemberID, "reset@example.com", RoleUser, 60)
	oldHash := testTokenHash(t, 61)
	if _, err := store.CreatePasswordResetTicket(
		context.Background(),
		admin.ID,
		member.ID,
		CreatePasswordResetTicketInput{TokenHash: oldHash, ExpiresAt: testNow.Add(time.Hour)},
		testNow.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	hash := testTokenHash(t, 61+1)
	ticket, err := store.CreatePasswordResetTicket(
		context.Background(),
		admin.ID,
		member.ID,
		CreatePasswordResetTicketInput{TokenHash: hash, ExpiresAt: testNow.Add(50 * time.Minute)},
		testNow.Add(3*time.Minute),
	)
	if err != nil || ticket.State != PasswordResetPending {
		t.Fatalf("create reset ticket = %#v, %v", ticket, err)
	}
	oldTicket, err := store.GetPasswordResetTicketByTokenHash(context.Background(), oldHash)
	if err != nil || oldTicket.State != PasswordResetRevoked {
		t.Fatalf("superseded reset ticket = %#v, %v", oldTicket, err)
	}
	currentTicket, err := store.GetPasswordResetTicketByTokenHash(context.Background(), hash)
	if err != nil || currentTicket.State != PasswordResetPending || currentTicket.UserID != member.ID {
		t.Fatalf("current reset ticket = %#v, %v", currentTicket, err)
	}
}

func TestSchemaRoleAndStateChecks(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	_, err := store.db.ExecContext(context.Background(),
		"UPDATE users SET role = ? WHERE id = ?", "superuser' OR 1=1 --", admin.ID)
	if err == nil {
		t.Fatal("schema accepted invalid role")
	}
	_, err = store.db.ExecContext(context.Background(),
		"UPDATE users SET state = ? WHERE id = ?", "deleted", admin.ID)
	if err == nil {
		t.Fatal("schema accepted invalid state")
	}
	if _, err := store.GetUser(context.Background(), " "+admin.ID); err == nil {
		t.Fatal("user lookup accepted a non-canonical id")
	}
}

func TestOperationTimesUseCanonicalMillisecondUTC(t *testing.T) {
	store := openTestStore(t)
	now := testNow.In(time.FixedZone("test", 9*60*60)).Add(987654 * time.Nanosecond)
	admin, err := store.BootstrapFirstAdmin(
		context.Background(),
		testNewUser(t, testAdminID, "time@example.com", 70),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := time.UnixMilli(now.UnixMilli()).UTC()
	if !admin.CreatedAt.Equal(want) || admin.CreatedAt.Location() != time.UTC {
		t.Fatalf("bootstrap time = %s (%s), want %s UTC", admin.CreatedAt, admin.CreatedAt.Location(), want)
	}
	stored, err := store.GetUser(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.Equal(admin.CreatedAt) {
		t.Fatalf("stored time = %s, returned time = %s", stored.CreatedAt, admin.CreatedAt)
	}
}

func TestUserAndVaultIDsMustPrecedeEnvelopeCreation(t *testing.T) {
	store := openTestStore(t)
	input := testNewUser(t, testAdminID, "binding@example.com", 71)
	input.ID = ""
	if _, err := store.BootstrapFirstAdmin(context.Background(), input, testNow); err == nil {
		t.Fatal("bootstrap silently generated an ID after envelope creation")
	}
	input = testNewUser(t, testAdminID, "binding@example.com", 72)
	input.VaultID = ""
	if _, err := store.BootstrapFirstAdmin(context.Background(), input, testNow); err == nil {
		t.Fatal("bootstrap accepted an empty envelope VaultID binding")
	}
	input = testNewUser(t, testAdminID, "binding@example.com", 73)
	input.RecoveryEnvelope = nil
	if _, err := store.BootstrapFirstAdmin(context.Background(), input, testNow); err == nil {
		t.Fatal("bootstrap accepted an account without a recovery envelope")
	}
}
