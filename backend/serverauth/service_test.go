package serverauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

var serverAuthTestNow = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

const (
	serverAuthTestUserID  = "serverauth_user_12345"
	serverAuthTestVaultID = "serverauth_vault_1234"
)

type fakeControlStore struct {
	isBootstrappedFn                    func(context.Context) (bool, error)
	bootstrapFirstAdminFn               func(context.Context, control.BootstrapAdminInput, time.Time) (control.UserSummary, error)
	getUserFn                           func(context.Context, string) (control.UserSummary, error)
	getUserByEmailFn                    func(context.Context, string) (control.UserSummary, error)
	lookupVaultIDFn                     func(context.Context, string) (string, error)
	getPasswordCredentialFn             func(context.Context, string) (control.PasswordCredential, error)
	getActiveRecoveryEnvelopeFn         func(context.Context, string) (control.RecoveryEnvelope, error)
	recordSuccessfulLoginFn             func(context.Context, string, control.PasswordCredential, time.Time) error
	createInvitationFn                  func(context.Context, string, control.CreateInvitationInput, time.Time) (control.Invitation, error)
	getInvitationByTokenHashFn          func(context.Context, []byte) (control.Invitation, error)
	acceptInvitationFn                  func(context.Context, []byte, control.NewUserInput, time.Time) (control.UserSummary, error)
	createPasswordResetTicketFn         func(context.Context, string, string, control.CreatePasswordResetTicketInput, time.Time) (control.PasswordResetTicket, error)
	getPasswordResetTicketByTokenHashFn func(context.Context, []byte) (control.PasswordResetTicket, error)
	completePasswordResetFn             func(context.Context, control.CompletePasswordResetInput, time.Time) (control.PasswordResetTicket, error)
	disableUserFn                       func(context.Context, string, string, time.Time) error
}

func (f *fakeControlStore) IsBootstrapped(ctx context.Context) (bool, error) {
	if f.isBootstrappedFn == nil {
		panic("unexpected IsBootstrapped call")
	}
	return f.isBootstrappedFn(ctx)
}

func (f *fakeControlStore) BootstrapFirstAdmin(ctx context.Context, input control.BootstrapAdminInput, now time.Time) (control.UserSummary, error) {
	if f.bootstrapFirstAdminFn == nil {
		panic("unexpected BootstrapFirstAdmin call")
	}
	return f.bootstrapFirstAdminFn(ctx, input, now)
}

func (f *fakeControlStore) GetUser(ctx context.Context, userID string) (control.UserSummary, error) {
	if f.getUserFn == nil {
		panic("unexpected GetUser call")
	}
	return f.getUserFn(ctx, userID)
}

func (f *fakeControlStore) GetUserByEmail(ctx context.Context, email string) (control.UserSummary, error) {
	if f.getUserByEmailFn == nil {
		panic("unexpected GetUserByEmail call")
	}
	return f.getUserByEmailFn(ctx, email)
}

func (f *fakeControlStore) LookupVaultID(ctx context.Context, userID string) (string, error) {
	if f.lookupVaultIDFn == nil {
		panic("unexpected LookupVaultID call")
	}
	return f.lookupVaultIDFn(ctx, userID)
}

func (f *fakeControlStore) GetPasswordCredential(ctx context.Context, userID string) (control.PasswordCredential, error) {
	if f.getPasswordCredentialFn == nil {
		panic("unexpected GetPasswordCredential call")
	}
	return f.getPasswordCredentialFn(ctx, userID)
}

func (f *fakeControlStore) GetActiveRecoveryEnvelope(ctx context.Context, userID string) (control.RecoveryEnvelope, error) {
	if f.getActiveRecoveryEnvelopeFn == nil {
		panic("unexpected GetActiveRecoveryEnvelope call")
	}
	return f.getActiveRecoveryEnvelopeFn(ctx, userID)
}

func (f *fakeControlStore) RecordSuccessfulLogin(ctx context.Context, userID string, expected control.PasswordCredential, now time.Time) error {
	if f.recordSuccessfulLoginFn == nil {
		panic("unexpected RecordSuccessfulLogin call")
	}
	return f.recordSuccessfulLoginFn(ctx, userID, expected, now)
}

func (f *fakeControlStore) CreateInvitation(ctx context.Context, actorID string, input control.CreateInvitationInput, now time.Time) (control.Invitation, error) {
	if f.createInvitationFn == nil {
		panic("unexpected CreateInvitation call")
	}
	return f.createInvitationFn(ctx, actorID, input, now)
}

func (f *fakeControlStore) GetInvitationByTokenHash(ctx context.Context, tokenHash []byte) (control.Invitation, error) {
	if f.getInvitationByTokenHashFn == nil {
		panic("unexpected GetInvitationByTokenHash call")
	}
	return f.getInvitationByTokenHashFn(ctx, tokenHash)
}

func (f *fakeControlStore) AcceptInvitation(ctx context.Context, tokenHash []byte, input control.NewUserInput, now time.Time) (control.UserSummary, error) {
	if f.acceptInvitationFn == nil {
		panic("unexpected AcceptInvitation call")
	}
	return f.acceptInvitationFn(ctx, tokenHash, input, now)
}

func (f *fakeControlStore) CreatePasswordResetTicket(ctx context.Context, actorID, userID string, input control.CreatePasswordResetTicketInput, now time.Time) (control.PasswordResetTicket, error) {
	if f.createPasswordResetTicketFn == nil {
		panic("unexpected CreatePasswordResetTicket call")
	}
	return f.createPasswordResetTicketFn(ctx, actorID, userID, input, now)
}

func (f *fakeControlStore) GetPasswordResetTicketByTokenHash(ctx context.Context, tokenHash []byte) (control.PasswordResetTicket, error) {
	if f.getPasswordResetTicketByTokenHashFn == nil {
		panic("unexpected GetPasswordResetTicketByTokenHash call")
	}
	return f.getPasswordResetTicketByTokenHashFn(ctx, tokenHash)
}

func (f *fakeControlStore) CompletePasswordReset(ctx context.Context, input control.CompletePasswordResetInput, now time.Time) (control.PasswordResetTicket, error) {
	if f.completePasswordResetFn == nil {
		panic("unexpected CompletePasswordReset call")
	}
	return f.completePasswordResetFn(ctx, input, now)
}

func (f *fakeControlStore) DisableUser(ctx context.Context, actorID, targetUserID string, now time.Time) error {
	if f.disableUserFn == nil {
		panic("unexpected DisableUser call")
	}
	return f.disableUserFn(ctx, actorID, targetUserID, now)
}

type fakeSessionInvalidator struct {
	users    []string
	result   int
	onDelete func(string)
}

func (f *fakeSessionInvalidator) DeleteAllSessionsForUser(userID string) int {
	f.users = append(f.users, userID)
	if f.onDelete != nil {
		f.onDelete(userID)
	}
	return f.result
}

type fakeVaultDrainer struct {
	users    []string
	beginErr error
	waitErr  error
	onBegin  func(string)
	onWait   func(string)
}

func (f *fakeVaultDrainer) BeginUserDrain(userID string) (func(context.Context) error, error) {
	f.users = append(f.users, userID)
	if f.onBegin != nil {
		f.onBegin(userID)
	}
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return func(context.Context) error {
		if f.onWait != nil {
			f.onWait(userID)
		}
		return f.waitErr
	}, nil
}

func newServerAuthTestService(
	t *testing.T,
	store ControlStore,
	setup *SetupAuthorizer,
	openSession OpenSessionFunc,
	sessions SessionInvalidator,
	vaults VaultDrainer,
) *Service {
	t.Helper()
	if openSession == nil {
		openSession = func(control.UserSummary, string, *securedb.RawKey) (*middleware.Session, error) {
			panic("unexpected OpenSession call")
		}
	}
	if sessions == nil {
		sessions = &fakeSessionInvalidator{}
	}
	if vaults == nil {
		vaults = &fakeVaultDrainer{}
	}
	service, err := NewService(Dependencies{
		Store:            store,
		OpenSession:      openSession,
		Sessions:         sessions,
		Vaults:           vaults,
		Setup:            setup,
		MaxConcurrentKDF: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestServiceBootstrapAuthorizesAndBindsBothEnvelopes(t *testing.T) {
	setupToken := []byte("serverauth_bootstrap_token_0123456789abcdef")
	authorizer := &SetupAuthorizer{digest: sha256.Sum256(setupToken)}
	bootstrapped := false
	bootstrapCalls := 0
	var captured control.BootstrapAdminInput
	want := control.UserSummary{
		ID:          serverAuthTestUserID,
		Email:       "admin@example.com",
		DisplayName: "Initial Admin",
		Role:        control.RoleAdmin,
		State:       control.UserActive,
	}
	store := &fakeControlStore{
		isBootstrappedFn: func(context.Context) (bool, error) {
			return bootstrapped, nil
		},
		bootstrapFirstAdminFn: func(_ context.Context, input control.BootstrapAdminInput, now time.Time) (control.UserSummary, error) {
			bootstrapCalls++
			captured = input
			if now != serverAuthTestNow {
				return control.UserSummary{}, fmt.Errorf("bootstrap time = %s", now)
			}
			bootstrapped = true
			return want, nil
		},
	}
	service := newServerAuthTestService(t, store, authorizer, nil, nil, nil)
	password := []byte("correct horse battery staple")
	recovery := bytes.Repeat([]byte{0x44}, keyenvelope.RecoverySecretSize)
	input := BootstrapInput{
		SetupToken:     []byte("wrong_bootstrap_token_0123456789abcdef"),
		Email:          want.Email,
		DisplayName:    want.DisplayName,
		Password:       password,
		RecoverySecret: recovery,
	}
	if _, err := service.Bootstrap(context.Background(), input, serverAuthTestNow); !errors.Is(err, ErrSetupUnauthorized) {
		t.Fatalf("unauthorized bootstrap error = %v", err)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("unauthorized bootstrap reached store %d times", bootstrapCalls)
	}

	input.SetupToken = setupToken
	got, err := service.Bootstrap(context.Background(), input, serverAuthTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap result = %#v, want %#v", got, want)
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap store calls = %d, want 1", bootstrapCalls)
	}
	if captured.ID == "" || captured.VaultID == "" || captured.ID == captured.VaultID {
		t.Fatalf("generated account identifiers are invalid: user=%q vault=%q", captured.ID, captured.VaultID)
	}
	if captured.Email != input.Email || captured.DisplayName != input.DisplayName || captured.RecoveryEnvelope == nil {
		t.Fatalf("captured bootstrap input = %#v", captured)
	}
	binding := keyenvelope.Context{UserID: captured.ID, VaultID: captured.VaultID}
	fromPassword, err := keyenvelope.UnwrapWithPassword(&captured.PasswordCredential.Envelope, password, binding)
	if err != nil {
		t.Fatalf("unwrap bootstrap password envelope: %v", err)
	}
	defer clear(fromPassword)
	fromRecovery, err := keyenvelope.UnwrapWithRecovery(&captured.RecoveryEnvelope.Envelope, recovery, binding)
	if err != nil {
		t.Fatalf("unwrap bootstrap recovery envelope: %v", err)
	}
	defer clear(fromRecovery)
	if !bytes.Equal(fromPassword, fromRecovery) {
		t.Fatal("password and recovery envelopes do not wrap the same vault DEK")
	}

	if _, err := service.Bootstrap(context.Background(), input, serverAuthTestNow.Add(time.Second)); !errors.Is(err, ErrAlreadySetup) {
		t.Fatalf("second bootstrap error = %v", err)
	}
	if bootstrapCalls != 1 {
		t.Fatalf("second bootstrap reached mutation store; calls = %d", bootstrapCalls)
	}
}

func TestServiceLoginSuccessFailureAndCredentialCAS(t *testing.T) {
	password := []byte("correct horse battery staple")
	dek := bytes.Repeat([]byte{0x57}, keyenvelope.DEKSize)
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, password, binding)
	if err != nil {
		t.Fatal(err)
	}
	user := control.UserSummary{
		ID:          serverAuthTestUserID,
		Email:       "person@example.com",
		DisplayName: "Person",
		Role:        control.RoleUser,
		State:       control.UserActive,
	}
	credential := control.PasswordCredential{
		UserID:    user.ID,
		Envelope:  *passwordEnvelope,
		CreatedAt: serverAuthTestNow.Add(-time.Hour),
		UpdatedAt: serverAuthTestNow.Add(-time.Minute),
	}
	recordErr := error(nil)
	recordCalls := 0
	openCalls := 0
	store := &fakeControlStore{
		getUserByEmailFn: func(_ context.Context, email string) (control.UserSummary, error) {
			if email != user.Email {
				return control.UserSummary{}, control.ErrNotFound
			}
			return user, nil
		},
		getUserFn: func(_ context.Context, userID string) (control.UserSummary, error) {
			if userID != user.ID {
				return control.UserSummary{}, control.ErrNotFound
			}
			return user, nil
		},
		getPasswordCredentialFn: func(_ context.Context, userID string) (control.PasswordCredential, error) {
			if userID != user.ID {
				return control.PasswordCredential{}, control.ErrNotFound
			}
			return credential, nil
		},
		lookupVaultIDFn: func(_ context.Context, userID string) (string, error) {
			if userID != user.ID {
				return "", control.ErrNotFound
			}
			return serverAuthTestVaultID, nil
		},
		recordSuccessfulLoginFn: func(_ context.Context, userID string, expected control.PasswordCredential, now time.Time) error {
			recordCalls++
			if userID != user.ID || !reflect.DeepEqual(expected, credential) || now != serverAuthTestNow {
				return fmt.Errorf("unexpected login CAS input")
			}
			return recordErr
		},
	}
	wantSession := &middleware.Session{ID: "serverauth-session-id", UserID: user.ID}
	openSession := func(gotUser control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
		defer key.Destroy()
		openCalls++
		if recordCalls == 0 {
			return nil, errors.New("vault opened before successful-login CAS")
		}
		if key == nil || !reflect.DeepEqual(gotUser, user) || vaultID != serverAuthTestVaultID || !bytes.Equal(key[:], dek) {
			return nil, errors.New("open session received wrong account or vault key")
		}
		return wantSession, nil
	}
	service := newServerAuthTestService(t, store, nil, openSession, nil, nil)

	gotSession, err := service.Login(context.Background(), user.Email, password, serverAuthTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if gotSession != wantSession || recordCalls != 1 || openCalls != 1 {
		t.Fatalf("successful login: session=%p record=%d open=%d", gotSession, recordCalls, openCalls)
	}

	recordCallsBefore, openCallsBefore := recordCalls, openCalls
	if _, err := service.Login(context.Background(), user.Email, []byte("incorrect password value"), serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong-password login error = %v", err)
	}
	if recordCalls != recordCallsBefore || openCalls != openCallsBefore {
		t.Fatalf("wrong-password login committed or opened vault: record=%d open=%d", recordCalls, openCalls)
	}

	recordErr = control.ErrCredentialConflict
	if _, err := service.Login(context.Background(), user.Email, password, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("credential-CAS login error = %v", err)
	}
	if recordCalls != recordCallsBefore+1 || openCalls != openCallsBefore {
		t.Fatalf("credential-CAS conflict opened vault: record=%d open=%d", recordCalls, openCalls)
	}
}

func TestServiceCompletePasswordResetInvalidatesSessionsThenClosesVault(t *testing.T) {
	rawToken := bytes.Repeat([]byte{0x6a}, bearerTokenBytes)
	encodedToken := base64.RawURLEncoding.EncodeToString(rawToken)
	wantHash, err := control.HashBearerToken(rawToken)
	if err != nil {
		t.Fatal(err)
	}
	oldRecoverySecret := bytes.Repeat([]byte{0x71}, keyenvelope.RecoverySecretSize)
	newRecoverySecret := bytes.Repeat([]byte{0x72}, keyenvelope.RecoverySecretSize)
	newPassword := []byte("the replacement password")
	dek := bytes.Repeat([]byte{0x73}, keyenvelope.DEKSize)
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	oldRecoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, oldRecoverySecret, binding)
	if err != nil {
		t.Fatal(err)
	}
	user := control.UserSummary{ID: serverAuthTestUserID, State: control.UserActive}
	expectedRecovery := control.RecoveryEnvelope{
		ID:        "serverauth_recovery_1",
		UserID:    user.ID,
		Envelope:  *oldRecoveryEnvelope,
		State:     control.RecoveryEnvelopeActive,
		CreatedAt: serverAuthTestNow.Add(-time.Hour),
	}
	ticket := control.PasswordResetTicket{
		ID:        "serverauth_reset_ticket_1",
		UserID:    user.ID,
		State:     control.PasswordResetPending,
		CreatedAt: serverAuthTestNow.Add(-time.Minute),
		ExpiresAt: serverAuthTestNow.Add(time.Hour),
	}
	resolvedAt := serverAuthTestNow
	resolved := ticket
	resolved.State = control.PasswordResetConsumed
	resolved.ResolvedAt = &resolvedAt
	events := []string{}
	var captured control.CompletePasswordResetInput
	completeErr := error(nil)
	store := &fakeControlStore{
		getPasswordResetTicketByTokenHashFn: func(_ context.Context, tokenHash []byte) (control.PasswordResetTicket, error) {
			if !bytes.Equal(tokenHash, wantHash) {
				return control.PasswordResetTicket{}, errors.New("reset lookup received wrong token hash")
			}
			return ticket, nil
		},
		getUserFn: func(_ context.Context, userID string) (control.UserSummary, error) {
			if userID != user.ID {
				return control.UserSummary{}, control.ErrNotFound
			}
			return user, nil
		},
		lookupVaultIDFn: func(_ context.Context, userID string) (string, error) {
			if userID != user.ID {
				return "", control.ErrNotFound
			}
			return serverAuthTestVaultID, nil
		},
		getActiveRecoveryEnvelopeFn: func(_ context.Context, userID string) (control.RecoveryEnvelope, error) {
			if userID != user.ID {
				return control.RecoveryEnvelope{}, control.ErrNotFound
			}
			return expectedRecovery, nil
		},
		completePasswordResetFn: func(_ context.Context, input control.CompletePasswordResetInput, now time.Time) (control.PasswordResetTicket, error) {
			events = append(events, "complete")
			captured = input
			captured.TokenHash = bytes.Clone(input.TokenHash)
			if now != serverAuthTestNow {
				return control.PasswordResetTicket{}, errors.New("reset received wrong time")
			}
			if completeErr != nil {
				return control.PasswordResetTicket{}, completeErr
			}
			return resolved, nil
		},
	}
	sessions := &fakeSessionInvalidator{result: 2, onDelete: func(string) {
		events = append(events, "invalidate")
	}}
	vaults := &fakeVaultDrainer{onBegin: func(string) {
		events = append(events, "drain")
	}, onWait: func(string) {
		events = append(events, "close")
	}}
	service := newServerAuthTestService(t, store, nil, nil, sessions, vaults)
	input := CompletePasswordResetInput{
		Token:             encodedToken,
		RecoverySecret:    oldRecoverySecret,
		NewPassword:       newPassword,
		NewRecoverySecret: newRecoverySecret,
	}
	got, err := service.CompletePasswordReset(context.Background(), input, serverAuthTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, resolved) {
		t.Fatalf("reset result = %#v, want %#v", got, resolved)
	}
	if !reflect.DeepEqual(events, []string{"complete", "drain", "invalidate", "close"}) {
		t.Fatalf("reset side-effect order = %v", events)
	}
	if !reflect.DeepEqual(sessions.users, []string{user.ID}) || !reflect.DeepEqual(vaults.users, []string{user.ID}) {
		t.Fatalf("reset affected wrong users: sessions=%v vaults=%v", sessions.users, vaults.users)
	}
	if !bytes.Equal(captured.TokenHash, wantHash) || !reflect.DeepEqual(captured.ExpectedRecoveryEnvelope, expectedRecovery) {
		t.Fatal("reset mutation did not carry the hashed token and expected recovery CAS")
	}
	fromPassword, err := keyenvelope.UnwrapWithPassword(&captured.PasswordCredential.Envelope, newPassword, binding)
	if err != nil {
		t.Fatalf("unwrap replacement password envelope: %v", err)
	}
	defer clear(fromPassword)
	fromRecovery, err := keyenvelope.UnwrapWithRecovery(&captured.RecoveryEnvelope.Envelope, newRecoverySecret, binding)
	if err != nil {
		t.Fatalf("unwrap replacement recovery envelope: %v", err)
	}
	defer clear(fromRecovery)
	if !bytes.Equal(fromPassword, dek) || !bytes.Equal(fromRecovery, dek) {
		t.Fatal("reset replacement envelopes do not preserve the user's vault DEK")
	}

	mutationFailure := errors.New("reset mutation failed")
	completeErr = mutationFailure
	events = nil
	sessions.users = nil
	vaults.users = nil
	if _, err := service.CompletePasswordReset(context.Background(), input, serverAuthTestNow); !errors.Is(err, mutationFailure) {
		t.Fatalf("failed reset error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"complete"}) || len(sessions.users) != 0 || len(vaults.users) != 0 {
		t.Fatalf("failed reset invalidated live state: events=%v sessions=%v vaults=%v", events, sessions.users, vaults.users)
	}
}

func TestServiceDisableUserCommitsBeforeInvalidationAndVaultClose(t *testing.T) {
	actorID := "serverauth_admin_12345"
	targetID := serverAuthTestUserID
	events := []string{}
	disableErr := error(nil)
	store := &fakeControlStore{
		disableUserFn: func(_ context.Context, gotActorID, gotTargetID string, now time.Time) error {
			events = append(events, "disable")
			if gotActorID != actorID || gotTargetID != targetID || now != serverAuthTestNow {
				return errors.New("disable received wrong input")
			}
			return disableErr
		},
	}
	sessions := &fakeSessionInvalidator{onDelete: func(string) {
		events = append(events, "invalidate")
	}}
	vaults := &fakeVaultDrainer{onBegin: func(string) {
		events = append(events, "drain")
	}, onWait: func(string) {
		events = append(events, "close")
	}}
	service := newServerAuthTestService(t, store, nil, nil, sessions, vaults)

	storeFailure := errors.New("disable mutation failed")
	disableErr = storeFailure
	if err := service.DisableUser(context.Background(), actorID, targetID, serverAuthTestNow); !errors.Is(err, storeFailure) {
		t.Fatalf("failed disable error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"disable"}) || len(sessions.users) != 0 || len(vaults.users) != 0 {
		t.Fatalf("failed disable invalidated live state: events=%v sessions=%v vaults=%v", events, sessions.users, vaults.users)
	}

	disableErr = nil
	events = nil
	if err := service.DisableUser(context.Background(), actorID, targetID, serverAuthTestNow); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"disable", "drain", "invalidate", "close"}) {
		t.Fatalf("disable side-effect order = %v", events)
	}
	if !reflect.DeepEqual(sessions.users, []string{targetID}) || !reflect.DeepEqual(vaults.users, []string{targetID}) {
		t.Fatalf("disable affected wrong users: sessions=%v vaults=%v", sessions.users, vaults.users)
	}

	vaultCloseFailure := errors.New("vault close failed")
	vaults.waitErr = vaultCloseFailure
	events = nil
	sessions.users = nil
	vaults.users = nil
	if err := service.DisableUser(context.Background(), actorID, targetID, serverAuthTestNow); err != nil {
		t.Fatalf("committed disable reported cleanup failure: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"disable", "drain", "invalidate", "close"}) || !reflect.DeepEqual(sessions.users, []string{targetID}) {
		t.Fatalf("vault-close failure lost invalidation: events=%v sessions=%v", events, sessions.users)
	}
}

func TestUnknownLoginsForSameCanonicalEmailAreSerialized(t *testing.T) {
	const canonicalEmail = "unknown@example.com"
	firstLookupEntered := make(chan struct{})
	releaseFirstLookup := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(releaseFirstLookup) }) }
	t.Cleanup(releaseFirst)

	var lookupCalls atomic.Int32
	store := &fakeControlStore{
		getUserByEmailFn: func(_ context.Context, email string) (control.UserSummary, error) {
			if email != canonicalEmail {
				return control.UserSummary{}, fmt.Errorf("lookup email = %q, want %q", email, canonicalEmail)
			}
			call := lookupCalls.Add(1)
			if call == 1 {
				close(firstLookupEntered)
				<-releaseFirstLookup
			}
			return control.UserSummary{}, control.ErrNotFound
		},
	}
	service := newServerAuthTestService(t, store, nil, nil, nil, nil)
	results := make(chan error, 2)
	go func() {
		_, err := service.Login(
			context.Background(), "  Unknown@Example.COM ", []byte("incorrect password value"), serverAuthTestNow,
		)
		results <- err
	}()
	select {
	case <-firstLookupEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first unknown login did not reach the account lookup")
	}

	go func() {
		_, err := service.Login(
			context.Background(), canonicalEmail, []byte("incorrect password value"), serverAuthTestNow,
		)
		results <- err
	}()

	lockKey := "email:" + canonicalEmail
	waitForServerAuthCondition(t, 2*time.Second, func() bool {
		service.locksMu.Lock()
		defer service.locksMu.Unlock()
		entry := service.locks[lockKey]
		return entry != nil && entry.refs == 2
	}, "second canonical-email login did not join the first login lock")
	if calls := lookupCalls.Load(); calls != 1 {
		t.Fatalf("unknown account lookup ran concurrently %d times, want 1", calls)
	}

	releaseFirst()
	for index := 0; index < 2; index++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("unknown login error = %v, want ErrInvalidCredentials", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serialized unknown login did not complete")
		}
	}
	if calls := lookupCalls.Load(); calls != 2 {
		t.Fatalf("unknown account lookup calls = %d, want 2", calls)
	}
	waitForServerAuthCondition(t, time.Second, func() bool {
		service.locksMu.Lock()
		defer service.locksMu.Unlock()
		_, exists := service.locks[lockKey]
		return !exists
	}, "canonical-email lock entry was retained after both logins")
}

func TestReserveKDFFailsImmediatelyWhenFullAndPreservesCanceledContext(t *testing.T) {
	service := &Service{kdfSlots: make(chan struct{}, 1)}
	release, err := service.reserveKDF(context.Background())
	if err != nil {
		t.Fatalf("reserve first KDF slot: %v", err)
	}

	fullResult := make(chan error, 1)
	go func() {
		_, err := service.reserveKDF(context.Background())
		fullResult <- err
	}()
	select {
	case err := <-fullResult:
		if !errors.Is(err, ErrAuthenticationBusy) {
			t.Fatalf("full KDF reservation error = %v, want ErrAuthenticationBusy", err)
		}
	case <-time.After(time.Second):
		// Release the first slot so a blocking implementation can unwind instead
		// of leaking its test goroutine after this regression is reported.
		release()
		<-fullResult
		t.Fatal("full KDF reservation blocked instead of failing immediately")
	}
	release()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.reserveKDF(canceled)
	if !errors.Is(err, ErrAuthenticationBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled KDF reservation error = %v, want ErrAuthenticationBusy and context.Canceled", err)
	}
	if used := len(service.kdfSlots); used != 0 {
		t.Fatalf("canceled KDF reservation consumed %d slots, want 0", used)
	}
}

func TestLoginBusyDoesNotConsultAccountStore(t *testing.T) {
	var lookups atomic.Int32
	store := &fakeControlStore{
		getUserByEmailFn: func(context.Context, string) (control.UserSummary, error) {
			lookups.Add(1)
			return control.UserSummary{}, control.ErrNotFound
		},
	}
	service := newServerAuthTestService(t, store, nil, nil, nil, nil)
	release, err := service.reserveKDF(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, email := range []string{"known-shape@example.com", "not an email"} {
		if _, err := service.Login(
			context.Background(), email, []byte("password value long enough"), serverAuthTestNow,
		); !errors.Is(err, ErrAuthenticationBusy) {
			t.Fatalf("Login(%q) error = %v, want ErrAuthenticationBusy", email, err)
		}
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("busy login performed %d account lookups, want 0", got)
	}
}

type reauthDrainWaiter struct {
	waitStarted chan struct{}
	childDone   <-chan struct{}
	once        sync.Once
}

func (d *reauthDrainWaiter) BeginUserDrain(string) (func(context.Context) error, error) {
	return func(ctx context.Context) error {
		d.once.Do(func() { close(d.waitStarted) })
		select {
		case <-d.childDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

func TestPasswordResetReleasesAccountLockBeforeWaitingForReauthChild(t *testing.T) {
	rawToken := bytes.Repeat([]byte{0x7a}, bearerTokenBytes)
	encodedToken := base64.RawURLEncoding.EncodeToString(rawToken)
	oldRecoverySecret := bytes.Repeat([]byte{0x81}, keyenvelope.RecoverySecretSize)
	newRecoverySecret := bytes.Repeat([]byte{0x82}, keyenvelope.RecoverySecretSize)
	dek := bytes.Repeat([]byte{0x83}, keyenvelope.DEKSize)
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, oldRecoverySecret, binding)
	if err != nil {
		t.Fatal(err)
	}
	ticket := control.PasswordResetTicket{
		ID:        "serverauth_reset_deadlock_1",
		UserID:    serverAuthTestUserID,
		State:     control.PasswordResetPending,
		CreatedAt: serverAuthTestNow.Add(-time.Minute),
		ExpiresAt: serverAuthTestNow.Add(time.Hour),
	}
	recovery := control.RecoveryEnvelope{
		ID:        "serverauth_recovery_deadlock_1",
		UserID:    serverAuthTestUserID,
		Envelope:  *recoveryEnvelope,
		State:     control.RecoveryEnvelopeActive,
		CreatedAt: serverAuthTestNow.Add(-time.Hour),
	}
	store := &fakeControlStore{
		getPasswordResetTicketByTokenHashFn: func(context.Context, []byte) (control.PasswordResetTicket, error) {
			return ticket, nil
		},
		getUserFn: func(context.Context, string) (control.UserSummary, error) {
			return control.UserSummary{ID: serverAuthTestUserID, State: control.UserActive}, nil
		},
		lookupVaultIDFn: func(context.Context, string) (string, error) {
			return serverAuthTestVaultID, nil
		},
		getActiveRecoveryEnvelopeFn: func(context.Context, string) (control.RecoveryEnvelope, error) {
			return recovery, nil
		},
		completePasswordResetFn: func(context.Context, control.CompletePasswordResetInput, time.Time) (control.PasswordResetTicket, error) {
			resolved := ticket
			resolved.State = control.PasswordResetConsumed
			return resolved, nil
		},
	}
	reauthChildDone := make(chan struct{})
	drainer := &reauthDrainWaiter{waitStarted: make(chan struct{}), childDone: reauthChildDone}
	service := newServerAuthTestService(t, store, nil, nil, nil, drainer)

	resetContext, cancelReset := context.WithCancel(context.Background())
	defer cancelReset()
	resetResult := make(chan error, 1)
	go func() {
		_, err := service.CompletePasswordReset(resetContext, CompletePasswordResetInput{
			Token:             encodedToken,
			RecoverySecret:    oldRecoverySecret,
			NewPassword:       []byte("replacement password value"),
			NewRecoverySecret: newRecoverySecret,
		}, serverAuthTestNow)
		resetResult <- err
	}()
	select {
	case <-drainer.waitStarted:
	case <-time.After(5 * time.Second):
		cancelReset()
		t.Fatal("password reset did not begin waiting for vault drain")
	}

	reauthResult := make(chan error, 1)
	go func() {
		err := service.Reauthenticate(
			context.Background(), serverAuthTestUserID, []byte("short"), serverAuthTestNow,
		)
		close(reauthChildDone)
		reauthResult <- err
	}()
	select {
	case err := <-reauthResult:
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("reauth child error = %v, want ErrInvalidCredentials", err)
		}
	case <-time.After(3 * time.Second):
		// Break the drain wait so a lock-order regression does not strand either
		// goroutine after the failure is observed.
		cancelReset()
		<-reauthResult
		t.Fatal("reauth child could not acquire the account lock while reset waited for drain")
	}
	select {
	case err := <-resetResult:
		if err != nil {
			t.Fatalf("password reset error = %v", err)
		}
	case <-time.After(time.Second):
		cancelReset()
		t.Fatal("password reset did not finish after the reauth child released")
	}
}

func TestNilAndZeroServicePublicMethodsFailClosed(t *testing.T) {
	receivers := map[string]*Service{
		"nil":  nil,
		"zero": {},
	}
	for receiverName, service := range receivers {
		service := service
		t.Run(receiverName, func(t *testing.T) {
			assertUnavailable := func(operation string, err error) {
				t.Helper()
				if !errors.Is(err, ErrServiceUnavailable) {
					t.Fatalf("%s error = %v, want ErrServiceUnavailable", operation, err)
				}
			}
			_, err := service.Bootstrap(context.Background(), BootstrapInput{}, serverAuthTestNow)
			assertUnavailable("Bootstrap", err)
			_, err = service.AcceptInvitation(context.Background(), AcceptInvitationInput{}, serverAuthTestNow)
			assertUnavailable("AcceptInvitation", err)
			_, err = service.Login(context.Background(), "person@example.com", nil, serverAuthTestNow)
			assertUnavailable("Login", err)
			assertUnavailable("Reauthenticate", service.Reauthenticate(context.Background(), serverAuthTestUserID, nil, serverAuthTestNow))
			_, _, err = service.CreateInvitation(
				context.Background(), serverAuthTestUserID, "person@example.com", control.RoleUser,
				serverAuthTestNow.Add(time.Hour), serverAuthTestNow,
			)
			assertUnavailable("CreateInvitation", err)
			_, _, err = service.CreatePasswordReset(
				context.Background(), serverAuthTestUserID, serverAuthTestUserID,
				serverAuthTestNow.Add(time.Hour), serverAuthTestNow,
			)
			assertUnavailable("CreatePasswordReset", err)
			_, err = service.CompletePasswordReset(context.Background(), CompletePasswordResetInput{}, serverAuthTestNow)
			assertUnavailable("CompletePasswordReset", err)
			assertUnavailable(
				"DisableUser",
				service.DisableUser(context.Background(), serverAuthTestUserID, serverAuthTestUserID, serverAuthTestNow),
			)
		})
	}
}

func waitForServerAuthCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatal(message)
	}
}
