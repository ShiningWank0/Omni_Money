package serverauth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

const (
	minimumPasswordBytes  = 12
	maximumPasswordBytes  = 1024
	defaultKDFConcurrency = 2
	maximumKDFConcurrency = 16
)

var (
	ErrInvalidCredentials = errors.New("invalid account credentials")
	ErrInvalidRecovery    = errors.New("invalid recovery credentials")
	ErrInvalidPassword    = errors.New("password must contain between 12 and 1024 bytes")
	ErrInvalidAccountData = errors.New("account setup data is invalid")
	ErrAuthenticationBusy = errors.New("password authentication capacity is busy")
	ErrServiceUnavailable = errors.New("server account service is unavailable")
)

var invalidPasswordWork = []byte("omni-money-invalid-password-work-factor")

// ControlStore is the control-plane capability required by Service. Financial
// data and raw vault keys are intentionally absent from this interface.
type ControlStore interface {
	IsBootstrapped(context.Context) (bool, error)
	BootstrapFirstAdmin(context.Context, control.BootstrapAdminInput, time.Time) (control.UserSummary, error)
	GetUser(context.Context, string) (control.UserSummary, error)
	GetUserByEmail(context.Context, string) (control.UserSummary, error)
	LookupVaultID(context.Context, string) (string, error)
	GetPasswordCredential(context.Context, string) (control.PasswordCredential, error)
	GetActiveRecoveryEnvelope(context.Context, string) (control.RecoveryEnvelope, error)
	RecordSuccessfulLogin(context.Context, string, control.PasswordCredential, time.Time) error
	CreateInvitation(context.Context, string, control.CreateInvitationInput, time.Time) (control.Invitation, error)
	GetInvitationByTokenHash(context.Context, []byte) (control.Invitation, error)
	AcceptInvitation(context.Context, []byte, control.NewUserInput, time.Time) (control.UserSummary, error)
	CreatePasswordResetTicket(context.Context, string, string, control.CreatePasswordResetTicketInput, time.Time) (control.PasswordResetTicket, error)
	GetPasswordResetTicketByTokenHash(context.Context, []byte) (control.PasswordResetTicket, error)
	CompletePasswordReset(context.Context, control.CompletePasswordResetInput, time.Time) (control.PasswordResetTicket, error)
	DisableUser(context.Context, string, string, time.Time) error
}

type SessionInvalidator interface {
	DeleteAllSessionsForUser(string) int
}

type VaultDrainer interface {
	BeginUserDrain(string) (func(context.Context) error, error)
}

// OpenSessionFunc consumes key to acquire a root vault lease and transfer it
// to SessionManager.CreateVaultSession. It must destroy key before returning,
// must not retain it, and must release a root lease on every session-creation
// failure. Service also destroys key defensively after the callback returns.
type OpenSessionFunc func(control.UserSummary, string, *securedb.RawKey) (*middleware.Session, error)

type Dependencies struct {
	Store            ControlStore
	OpenSession      OpenSessionFunc
	Sessions         SessionInvalidator
	Vaults           VaultDrainer
	Setup            *SetupAuthorizer
	MaxConcurrentKDF int
}

// Service is the sole mutation coordinator for server account lifecycle. All
// login, reset, and disable operations for a user share the same keyed lock so
// session issuance cannot race credential revocation.
type Service struct {
	store       ControlStore
	openSession OpenSessionFunc
	sessions    SessionInvalidator
	vaults      VaultDrainer
	setup       *SetupAuthorizer
	dummy       keyenvelope.Envelope
	kdfSlots    chan struct{}
	setupMu     sync.Mutex
	locksMu     sync.Mutex
	locks       map[string]*accountLock
}

type accountLock struct {
	gate chan struct{}
	refs int
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Store == nil || dependencies.OpenSession == nil || dependencies.Sessions == nil || dependencies.Vaults == nil {
		return nil, ErrServiceUnavailable
	}
	concurrency := dependencies.MaxConcurrentKDF
	if concurrency == 0 {
		concurrency = defaultKDFConcurrency
	}
	if concurrency < 1 || concurrency > maximumKDFConcurrency {
		return nil, fmt.Errorf("MaxConcurrentKDF must be between 1 and %d", maximumKDFConcurrency)
	}
	dummy, err := newDummyPasswordEnvelope()
	if err != nil {
		return nil, fmt.Errorf("initialize constant-work password verifier: %w", err)
	}
	return &Service{
		store:       dependencies.Store,
		openSession: dependencies.OpenSession,
		sessions:    dependencies.Sessions,
		vaults:      dependencies.Vaults,
		setup:       dependencies.Setup,
		dummy:       *dummy,
		kdfSlots:    make(chan struct{}, concurrency),
		locks:       make(map[string]*accountLock),
	}, nil
}

func (s *Service) ready() bool {
	return s != nil && s.store != nil && s.openSession != nil && s.sessions != nil &&
		s.vaults != nil && s.kdfSlots != nil && s.locks != nil
}

func newDummyPasswordEnvelope() (*keyenvelope.Envelope, error) {
	dek, err := keyenvelope.GenerateDEK()
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	return keyenvelope.WrapWithPassword(
		dek,
		invalidPasswordWork,
		keyenvelope.Context{UserID: "dummy-user-context", VaultID: "dummy-vault-context"},
	)
}

type BootstrapInput struct {
	SetupToken     []byte
	Email          string
	DisplayName    string
	Password       []byte
	RecoverySecret []byte
}

// Bootstrap creates exactly one initial administrator. RecoverySecret must be
// generated and saved by the client before submission, so a lost HTTP response
// cannot make the committed vault permanently unrecoverable.
func (s *Service) Bootstrap(ctx context.Context, input BootstrapInput, now time.Time) (control.UserSummary, error) {
	if !s.ready() {
		return control.UserSummary{}, ErrServiceUnavailable
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	bootstrapped, err := s.store.IsBootstrapped(ctx)
	if err != nil {
		return control.UserSummary{}, err
	}
	if bootstrapped {
		return control.UserSummary{}, ErrAlreadySetup
	}
	if s.setup == nil || !s.setup.Authorize(input.SetupToken) {
		return control.UserSummary{}, ErrSetupUnauthorized
	}
	if err := validateNewPassword(input.Password); err != nil {
		return control.UserSummary{}, err
	}
	if err := validateRecoverySecret(input.RecoverySecret); err != nil {
		return control.UserSummary{}, err
	}
	releaseKDF, err := s.reserveKDF(ctx)
	if err != nil {
		return control.UserSummary{}, err
	}
	account, err := prepareAccount(input.Email, input.DisplayName, input.Password, input.RecoverySecret)
	releaseKDF()
	if err != nil {
		return control.UserSummary{}, err
	}
	result, err := s.store.BootstrapFirstAdmin(ctx, account, now)
	if errors.Is(err, control.ErrAlreadyBootstrapped) {
		return control.UserSummary{}, ErrAlreadySetup
	}
	return result, err
}

type AcceptInvitationInput struct {
	Token          string
	DisplayName    string
	Password       []byte
	RecoverySecret []byte
}

func (s *Service) AcceptInvitation(ctx context.Context, input AcceptInvitationInput, now time.Time) (control.UserSummary, error) {
	if !s.ready() {
		return control.UserSummary{}, ErrServiceUnavailable
	}
	hash, err := HashEncodedBearerToken(input.Token)
	if err != nil {
		return control.UserSummary{}, control.ErrNotFound
	}
	defer clear(hash)
	invitation, err := s.store.GetInvitationByTokenHash(ctx, hash)
	if err != nil {
		return control.UserSummary{}, err
	}
	unlock, err := s.lockAccount("invitation:" + invitation.ID)
	if err != nil {
		return control.UserSummary{}, err
	}
	defer unlock()
	if err := validateNewPassword(input.Password); err != nil {
		return control.UserSummary{}, err
	}
	if err := validateRecoverySecret(input.RecoverySecret); err != nil {
		return control.UserSummary{}, err
	}
	releaseKDF, err := s.reserveKDF(ctx)
	if err != nil {
		return control.UserSummary{}, err
	}
	account, err := prepareAccount(invitation.Email, input.DisplayName, input.Password, input.RecoverySecret)
	releaseKDF()
	if err != nil {
		return control.UserSummary{}, err
	}
	return s.store.AcceptInvitation(ctx, hash, account, now)
}

func prepareAccount(email, displayName string, password, recoverySecret []byte) (control.NewUserInput, error) {
	userID, err := control.GenerateID()
	if err != nil {
		return control.NewUserInput{}, err
	}
	vaultID, err := control.GenerateID()
	if err != nil {
		return control.NewUserInput{}, err
	}
	dek, err := keyenvelope.GenerateDEK()
	if err != nil {
		return control.NewUserInput{}, err
	}
	defer clear(dek)
	binding := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, password, binding)
	if err != nil {
		return control.NewUserInput{}, err
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, recoverySecret, binding)
	if err != nil {
		return control.NewUserInput{}, err
	}
	return control.NewUserInput{
		ID:                 userID,
		Email:              email,
		DisplayName:        displayName,
		VaultID:            vaultID,
		PasswordCredential: control.PasswordCredentialInput{Envelope: *passwordEnvelope},
		RecoveryEnvelope:   &control.RecoveryEnvelopeInput{Envelope: *recoveryEnvelope},
	}, nil
}

func (s *Service) Login(ctx context.Context, email string, password []byte, now time.Time) (*middleware.Session, error) {
	if !s.ready() {
		return nil, ErrServiceUnavailable
	}
	email, emailErr := normalizeLoginEmail(email)
	if emailErr != nil {
		if err := s.runDummyPassword(ctx, password); err != nil {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}
	releaseKDF, err := s.reserveKDF(ctx)
	if err != nil {
		return nil, err
	}
	unlockEmail, err := s.lockAccount("email:" + email)
	if err != nil {
		releaseKDF()
		return nil, err
	}
	defer unlockEmail()
	user, lookupErr := s.store.GetUserByEmail(ctx, email)
	if lookupErr != nil {
		s.runDummyPasswordWork(password)
		releaseKDF()
		if errors.Is(lookupErr, control.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, lookupErr
	}
	unlock, err := s.lockAccount("user:" + user.ID)
	if err != nil {
		releaseKDF()
		return nil, err
	}
	defer unlock()
	user, err = s.store.GetUser(ctx, user.ID)
	if err != nil || user.State != control.UserActive {
		s.runDummyPasswordWork(password)
		releaseKDF()
		if err != nil && !errors.Is(err, control.ErrNotFound) {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}
	if validateNewPassword(password) != nil {
		s.runDummyPasswordWork(password)
		releaseKDF()
		return nil, ErrInvalidCredentials
	}
	credential, err := s.store.GetPasswordCredential(ctx, user.ID)
	if err != nil {
		releaseKDF()
		return nil, err
	}
	vaultID, err := s.store.LookupVaultID(ctx, user.ID)
	if err != nil {
		releaseKDF()
		return nil, err
	}
	dek, err := keyenvelope.UnwrapWithPassword(
		&credential.Envelope,
		password,
		keyenvelope.Context{UserID: user.ID, VaultID: vaultID},
	)
	releaseKDF()
	if err != nil {
		clear(dek)
		if errors.Is(err, keyenvelope.ErrAuthentication) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if err := s.store.RecordSuccessfulLogin(ctx, user.ID, credential, now); err != nil {
		clear(dek)
		if errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	key, err := securedb.NewRawKey(dek)
	clear(dek)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	session, err := s.openSession(user, vaultID, &key)
	if err != nil {
		return nil, fmt.Errorf("open authenticated vault session: %w", err)
	}
	if session == nil {
		return nil, errors.New("open authenticated vault session returned no session")
	}
	return session, nil
}

// Reauthenticate verifies the current credential revision without issuing a
// second root lease. The HTTP layer may rotate the existing session only after
// this method succeeds.
func (s *Service) Reauthenticate(ctx context.Context, userID string, password []byte, now time.Time) error {
	if !s.ready() {
		return ErrServiceUnavailable
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return err
	}
	defer unlock()
	if validateNewPassword(password) != nil {
		if err := s.runDummyPassword(ctx, password); err != nil {
			return err
		}
		return ErrInvalidCredentials
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil || user.State != control.UserActive {
		if dummyErr := s.runDummyPassword(ctx, password); dummyErr != nil {
			return dummyErr
		}
		return ErrInvalidCredentials
	}
	credential, err := s.store.GetPasswordCredential(ctx, userID)
	if err != nil {
		return err
	}
	vaultID, err := s.store.LookupVaultID(ctx, userID)
	if err != nil {
		return err
	}
	releaseKDF, err := s.reserveKDF(ctx)
	if err != nil {
		return err
	}
	verified, verifyErr := keyenvelope.VerifyPassword(
		&credential.Envelope, password, keyenvelope.Context{UserID: userID, VaultID: vaultID},
	)
	releaseKDF()
	if verifyErr != nil {
		return verifyErr
	}
	if !verified {
		return ErrInvalidCredentials
	}
	if err := s.store.RecordSuccessfulLogin(ctx, userID, credential, now); err != nil {
		if errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden) {
			return ErrInvalidCredentials
		}
		return err
	}
	return nil
}

func (s *Service) CreateInvitation(
	ctx context.Context,
	actorID, email string,
	role control.Role,
	expiresAt, now time.Time,
) (control.Invitation, string, error) {
	if !s.ready() {
		return control.Invitation{}, "", ErrServiceUnavailable
	}
	token, hash, err := GenerateBearerToken()
	if err != nil {
		return control.Invitation{}, "", err
	}
	defer clear(hash)
	invitation, err := s.store.CreateInvitation(ctx, actorID, control.CreateInvitationInput{
		Email: email, Role: role, TokenHash: hash, ExpiresAt: expiresAt,
	}, now)
	if err != nil {
		return control.Invitation{}, "", err
	}
	return invitation, token, nil
}

func (s *Service) CreatePasswordReset(
	ctx context.Context,
	actorID, targetUserID string,
	expiresAt, now time.Time,
) (control.PasswordResetTicket, string, error) {
	if !s.ready() {
		return control.PasswordResetTicket{}, "", ErrServiceUnavailable
	}
	token, hash, err := GenerateBearerToken()
	if err != nil {
		return control.PasswordResetTicket{}, "", err
	}
	defer clear(hash)
	ticket, err := s.store.CreatePasswordResetTicket(ctx, actorID, targetUserID,
		control.CreatePasswordResetTicketInput{TokenHash: hash, ExpiresAt: expiresAt}, now)
	if err != nil {
		return control.PasswordResetTicket{}, "", err
	}
	return ticket, token, nil
}

type CompletePasswordResetInput struct {
	Token             string
	RecoverySecret    []byte
	NewPassword       []byte
	NewRecoverySecret []byte
}

func (s *Service) CompletePasswordReset(
	ctx context.Context,
	input CompletePasswordResetInput,
	now time.Time,
) (control.PasswordResetTicket, error) {
	if !s.ready() {
		return control.PasswordResetTicket{}, ErrServiceUnavailable
	}
	hash, err := HashEncodedBearerToken(input.Token)
	if err != nil {
		return control.PasswordResetTicket{}, control.ErrNotFound
	}
	defer clear(hash)
	ticket, err := s.store.GetPasswordResetTicketByTokenHash(ctx, hash)
	if err != nil {
		return control.PasswordResetTicket{}, err
	}
	unlock, err := s.lockAccount("user:" + ticket.UserID)
	if err != nil {
		return control.PasswordResetTicket{}, err
	}
	result, waitForDrain, err := s.completePasswordResetLocked(ctx, input, hash, ticket, now)
	unlock()
	if err != nil {
		return control.PasswordResetTicket{}, err
	}
	s.finishDrain(ctx, waitForDrain)
	return result, nil
}

func (s *Service) completePasswordResetLocked(
	ctx context.Context,
	input CompletePasswordResetInput,
	hash []byte,
	ticket control.PasswordResetTicket,
	now time.Time,
) (control.PasswordResetTicket, func(context.Context) error, error) {
	if err := validateNewPassword(input.NewPassword); err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	if err := validateRecoverySecret(input.RecoverySecret); err != nil {
		return control.PasswordResetTicket{}, nil, ErrInvalidRecovery
	}
	if err := validateRecoverySecret(input.NewRecoverySecret); err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	user, err := s.store.GetUser(ctx, ticket.UserID)
	if err != nil || user.State != control.UserActive {
		return control.PasswordResetTicket{}, nil, ErrInvalidRecovery
	}
	vaultID, err := s.store.LookupVaultID(ctx, ticket.UserID)
	if err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	expectedRecovery, err := s.store.GetActiveRecoveryEnvelope(ctx, ticket.UserID)
	if err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	binding := keyenvelope.Context{UserID: ticket.UserID, VaultID: vaultID}
	passwordEnvelope, recoveryEnvelope, err := s.buildResetEnvelopes(ctx, input, expectedRecovery, binding)
	if err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	result, err := s.store.CompletePasswordReset(ctx, control.CompletePasswordResetInput{
		TokenHash:                hash,
		ExpectedRecoveryEnvelope: expectedRecovery,
		PasswordCredential:       control.PasswordCredentialInput{Envelope: *passwordEnvelope},
		RecoveryEnvelope:         control.RecoveryEnvelopeInput{Envelope: *recoveryEnvelope},
	}, now)
	if err != nil {
		return control.PasswordResetTicket{}, nil, err
	}
	waitForDrain, _ := s.vaults.BeginUserDrain(ticket.UserID)
	s.sessions.DeleteAllSessionsForUser(ticket.UserID)
	return result, waitForDrain, nil
}

func (s *Service) buildResetEnvelopes(
	ctx context.Context,
	input CompletePasswordResetInput,
	expectedRecovery control.RecoveryEnvelope,
	binding keyenvelope.Context,
) (*keyenvelope.Envelope, *keyenvelope.Envelope, error) {
	dek, err := keyenvelope.UnwrapWithRecovery(&expectedRecovery.Envelope, input.RecoverySecret, binding)
	if err != nil {
		clear(dek)
		if errors.Is(err, keyenvelope.ErrAuthentication) {
			return nil, nil, ErrInvalidRecovery
		}
		return nil, nil, err
	}
	defer clear(dek)
	releaseKDF, err := s.reserveKDF(ctx)
	if err != nil {
		return nil, nil, err
	}
	passwordEnvelope, passwordErr := keyenvelope.WrapWithPassword(dek, input.NewPassword, binding)
	releaseKDF()
	if passwordErr != nil {
		return nil, nil, passwordErr
	}
	recoveryEnvelope, err := keyenvelope.WrapWithRecovery(dek, input.NewRecoverySecret, binding)
	if err != nil {
		return nil, nil, err
	}
	return passwordEnvelope, recoveryEnvelope, nil
}

func (s *Service) DisableUser(ctx context.Context, actorID, targetUserID string, now time.Time) error {
	if !s.ready() {
		return ErrServiceUnavailable
	}
	unlock, err := s.lockAccount("user:" + targetUserID)
	if err != nil {
		return err
	}
	if err := s.store.DisableUser(ctx, actorID, targetUserID, now); err != nil {
		unlock()
		return err
	}
	waitForDrain, _ := s.vaults.BeginUserDrain(targetUserID)
	s.sessions.DeleteAllSessionsForUser(targetUserID)
	unlock()
	s.finishDrain(ctx, waitForDrain)
	return nil
}

// finishDrain waits only after the account lock has been released. If the
// request is canceled after the durable mutation, the final outstanding vault
// lease release completes the already-started drain; the committed operation
// is not misreported as a failure.
func (s *Service) finishDrain(ctx context.Context, wait func(context.Context) error) {
	if wait == nil {
		return
	}
	_ = wait(ctx)
}

func validateNewPassword(password []byte) error {
	if len(password) < minimumPasswordBytes || len(password) > maximumPasswordBytes {
		return ErrInvalidPassword
	}
	return nil
}

// normalizeLoginEmail mirrors the control store's canonical authentication
// lookup without exposing validation failures as distinguishable server
// errors. The store remains the final validator for every persisted email.
func normalizeLoginEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) < 3 || len(email) > 254 || strings.ContainsAny(email, "\r\n\x00") {
		return "", ErrInvalidCredentials
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.Count(email, "@") != 1 {
		return "", ErrInvalidCredentials
	}
	return email, nil
}

func validateRecoverySecret(secret []byte) error {
	if len(secret) != keyenvelope.RecoverySecretSize {
		return ErrInvalidAccountData
	}
	return nil
}

func (s *Service) runDummyPassword(ctx context.Context, password []byte) error {
	release, err := s.reserveKDF(ctx)
	if err != nil {
		return err
	}
	defer release()
	s.runDummyPasswordWork(password)
	return nil
}

// runDummyPasswordWork requires the caller to own one KDF slot. Keeping slot
// admission ahead of account lookup makes the busy response independent of
// whether an email exists in the control database.
func (s *Service) runDummyPasswordWork(password []byte) {
	candidate := password
	if validateNewPassword(password) != nil {
		candidate = invalidPasswordWork
	}
	dek, _ := keyenvelope.UnwrapWithPassword(
		&s.dummy,
		candidate,
		keyenvelope.Context{UserID: "dummy-user-context", VaultID: "dummy-vault-context"},
	)
	clear(dek)
}

func (s *Service) reserveKDF(ctx context.Context) (func(), error) {
	if s == nil || s.kdfSlots == nil {
		return nil, ErrServiceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(ErrAuthenticationBusy, err)
	}
	select {
	case s.kdfSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.kdfSlots }) }, nil
	default:
		return nil, ErrAuthenticationBusy
	}
}

// lockAccount has no waiter queue. Password work is deliberately expensive;
// blocking arbitrary requests on a mutex would let a distributed login flood
// accumulate unbounded goroutines for one account.
func (s *Service) lockAccount(key string) (func(), error) {
	s.locksMu.Lock()
	entry := s.locks[key]
	if entry == nil {
		entry = &accountLock{gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		s.locks[key] = entry
	}
	entry.refs++
	s.locksMu.Unlock()
	select {
	case <-entry.gate:
	default:
		s.locksMu.Lock()
		entry.refs--
		if entry.refs == 0 && s.locks[key] == entry {
			delete(s.locks, key)
		}
		s.locksMu.Unlock()
		return nil, ErrAuthenticationBusy
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.gate <- struct{}{}
			s.locksMu.Lock()
			entry.refs--
			if entry.refs == 0 && s.locks[key] == entry {
				delete(s.locks, key)
			}
			s.locksMu.Unlock()
		})
	}, nil
}
