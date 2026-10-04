package serverauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

const (
	passkeyCeremonyRegistration          = "registration"
	passkeyCeremonyRegistrationAssertion = "registration_assertion"
	passkeyCeremonyLogin                 = "login"
	passkeyCeremonyDiscoverableLogin     = "discoverable_login"
	passkeyCeremonyReauth                = "reauthentication"
	maxPasskeyCeremonies                 = 4096
)

// PRF outputs are bound to the credential. A stable, public, domain-separated
// input lets the browser evaluate the PRF before the user has been identified.
// The result remains secret and is required to unwrap the vault key.
var discoverablePRFSalt = sha256.Sum256([]byte("Omni Money WebAuthn PRF discoverable login v1"))

var (
	ErrPasskeysUnavailable = errors.New("passkey authentication is not configured")
	ErrPasskeyPRFRequired  = errors.New("this passkey does not support the WebAuthn PRF extension")
	ErrPasskeyCeremony     = errors.New("passkey ceremony is invalid or expired")
)

type passkeyCeremony struct {
	Kind      string
	UserID    string
	ClientKey string
	SessionID string
	Session   webauthn.SessionData
	PRFSalt   []byte
	// Candidate carries an attested but not yet persisted credential while the
	// registration assertion evaluates the registration PRF salt. It is kept in
	// memory only and becomes login-capable only when the assertion finish step
	// commits it with the vault envelope.
	Candidate *webauthn.Credential
	// ThrottleKey binds the ceremony to the account-level failure counter even
	// when the ceremony was issued for a decoy identity (empty UserID).
	ThrottleKey string
}

type PasskeyRegistrationBegin struct {
	CeremonyID string                       `json:"ceremony_id"`
	Options    *protocol.CredentialCreation `json:"options"`
}

type PasskeyLoginBegin struct {
	CeremonyID string                        `json:"ceremony_id"`
	Options    *protocol.CredentialAssertion `json:"options"`
}

type FinishPasskeyRegistrationInput struct {
	CeremonyID     string
	ClientKey      string
	Name           string
	Password       []byte
	CredentialJSON json.RawMessage
	PRFResult      []byte
}

type BeginPasskeyRegistrationAssertionInput struct {
	CeremonyID     string
	ClientKey      string
	CredentialJSON json.RawMessage
}

type FinishPasskeyRegistrationAssertionInput struct {
	CeremonyID     string
	ClientKey      string
	Name           string
	Password       []byte
	CredentialJSON json.RawMessage
	PRFResult      []byte
}

type FinishPasskeyLoginInput struct {
	CeremonyID     string
	ClientKey      string
	CredentialJSON json.RawMessage
	PRFResult      []byte
	Password       []byte
}

type webAuthnUser struct {
	user        control.UserSummary
	credentials []webauthn.Credential
}

func (u webAuthnUser) WebAuthnID() []byte                         { return []byte(u.user.ID) }
func (u webAuthnUser) WebAuthnName() string                       { return u.user.Email }
func (u webAuthnUser) WebAuthnDisplayName() string                { return u.user.DisplayName }
func (u webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) BeginPasskeyRegistration(ctx context.Context, userID, clientKey string) (PasskeyRegistrationBegin, error) {
	if !s.passkeysReady() {
		return PasskeyRegistrationBegin{}, ErrPasskeysUnavailable
	}
	user, records, err := s.loadPasskeyUser(ctx, userID)
	if err != nil {
		return PasskeyRegistrationBegin{}, err
	}
	if user.user.State != control.UserActive {
		return PasskeyRegistrationBegin{}, ErrInvalidCredentials
	}
	prfSalt := bytes.Clone(discoverablePRFSalt[:])
	creation, session, err := s.webauthn.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		// A bare PRF probe avoids asking providers to evaluate a newly created
		// credential before they have saved it. Evaluation follows in an assertion.
		webauthn.WithExtensions(webauthn.WithExtensionPRFSupport()),
	)
	if err != nil {
		clear(prfSalt)
		return PasskeyRegistrationBegin{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	_ = records // loadPasskeyUser already supplied exclusions through the adapter.
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: passkeyCeremonyRegistration, UserID: userID, ClientKey: clientKey, SessionID: passkeySessionID(ctx),
		Session: *session, PRFSalt: prfSalt,
	})
	clear(prfSalt)
	if err != nil {
		return PasskeyRegistrationBegin{}, err
	}
	return PasskeyRegistrationBegin{CeremonyID: ceremonyID, Options: creation}, nil
}

func (s *Service) FinishPasskeyRegistration(
	ctx context.Context,
	userID string,
	input FinishPasskeyRegistrationInput,
	now time.Time,
) (control.PasskeySummary, error) {
	if !s.passkeysReady() {
		return control.PasskeySummary{}, ErrPasskeysUnavailable
	}
	ceremony, err := s.takePasskeyCeremony(input.CeremonyID, passkeyCeremonyRegistration, input.ClientKey)
	if err != nil || ceremony.UserID != userID || ceremony.SessionID != passkeySessionID(ctx) {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	defer clear(ceremony.PRFSalt)
	if len(input.PRFResult) != 0 && len(input.PRFResult) != keyenvelope.PasskeySecretSize {
		return control.PasskeySummary{}, ErrPasskeyPRFRequired
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(input.CredentialJSON)
	if err != nil {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}

	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return control.PasskeySummary{}, err
	}
	defer unlock()
	user, _, err := s.loadPasskeyUser(ctx, userID)
	if err != nil || user.user.State != control.UserActive {
		return control.PasskeySummary{}, ErrInvalidCredentials
	}
	credential, err := s.webauthn.CreateCredential(user, ceremony.Session, parsed)
	if err != nil || credential == nil {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	return s.persistVerifiedPasskey(ctx, userID, input.Name, input.PRFResult, ceremony.PRFSalt, *credential, now)
}

// BeginPasskeyRegistrationAssertion continues a registration whose
// authenticator returned prf.enabled=true without a create-time PRF result.
// The create response is verified against the registration ceremony, then the
// candidate credential is bound to a fresh assertion challenge so the
// authenticator can evaluate the original registration salt. The candidate
// stays in memory and is not login-capable until the assertion finish step
// persists it with its vault envelope.
func (s *Service) BeginPasskeyRegistrationAssertion(
	ctx context.Context,
	userID string,
	input BeginPasskeyRegistrationAssertionInput,
) (PasskeyLoginBegin, error) {
	if !s.passkeysReady() {
		return PasskeyLoginBegin{}, ErrPasskeysUnavailable
	}
	ceremony, err := s.takePasskeyCeremony(input.CeremonyID, passkeyCeremonyRegistration, input.ClientKey)
	if err != nil || ceremony.UserID != userID || ceremony.SessionID != passkeySessionID(ctx) {
		return PasskeyLoginBegin{}, ErrPasskeyCeremony
	}
	defer clear(ceremony.PRFSalt)
	if len(input.CredentialJSON) == 0 {
		return PasskeyLoginBegin{}, ErrPasskeyCeremony
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(input.CredentialJSON)
	if err != nil {
		return PasskeyLoginBegin{}, ErrPasskeyCeremony
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	user, _, err := s.loadPasskeyUser(ctx, userID)
	if err != nil || user.user.State != control.UserActive {
		unlock()
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	credential, err := s.webauthn.CreateCredential(user, ceremony.Session, parsed)
	if err != nil || credential == nil {
		unlock()
		return PasskeyLoginBegin{}, ErrPasskeyCeremony
	}
	assertion, session, err := s.webauthn.BeginLogin(
		webAuthnUser{user: user.user, credentials: []webauthn.Credential{*credential}},
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(webauthn.WithExtensionPRF(protocol.PRFValues{
			First: protocol.URLEncodedBase64(bytes.Clone(ceremony.PRFSalt)),
		})),
	)
	unlock()
	if err != nil {
		return PasskeyLoginBegin{}, fmt.Errorf("begin passkey registration assertion: %w", err)
	}
	pendingSalt := bytes.Clone(ceremony.PRFSalt)
	defer clear(pendingSalt)
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: passkeyCeremonyRegistrationAssertion, UserID: userID, ClientKey: input.ClientKey, SessionID: ceremony.SessionID,
		Session: *session, PRFSalt: pendingSalt, Candidate: credential,
	})
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	return PasskeyLoginBegin{CeremonyID: ceremonyID, Options: assertion}, nil
}

// FinishPasskeyRegistrationAssertion verifies the candidate assertion
// (signature, challenge, RP ID, origin, user verification, credential ID)
// before wrapping the vault key with the PRF output and persisting the
// credential. A failure never leaves a registered credential behind.
func (s *Service) FinishPasskeyRegistrationAssertion(
	ctx context.Context,
	userID string,
	input FinishPasskeyRegistrationAssertionInput,
	now time.Time,
) (control.PasskeySummary, error) {
	if !s.passkeysReady() {
		return control.PasskeySummary{}, ErrPasskeysUnavailable
	}
	ceremony, err := s.takePasskeyCeremony(input.CeremonyID, passkeyCeremonyRegistrationAssertion, input.ClientKey)
	if err != nil || ceremony.UserID != userID || ceremony.SessionID != passkeySessionID(ctx) || ceremony.Candidate == nil {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	defer clear(ceremony.PRFSalt)
	if len(input.PRFResult) != 0 && len(input.PRFResult) != keyenvelope.PasskeySecretSize {
		return control.PasskeySummary{}, ErrPasskeyPRFRequired
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input.CredentialJSON)
	if err != nil {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return control.PasskeySummary{}, err
	}
	defer unlock()
	user, _, err := s.loadPasskeyUser(ctx, userID)
	if err != nil || user.user.State != control.UserActive {
		return control.PasskeySummary{}, ErrInvalidCredentials
	}
	candidate := *ceremony.Candidate
	updated, err := s.webauthn.ValidateLogin(
		webAuthnUser{user: user.user, credentials: []webauthn.Credential{candidate}},
		ceremony.Session, parsed,
	)
	if err != nil || updated == nil || updated.Authenticator.CloneWarning || !bytes.Equal(updated.ID, candidate.ID) {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	return s.persistVerifiedPasskey(ctx, userID, input.Name, input.PRFResult, ceremony.PRFSalt, *updated, now)
}

// Registration uses the authenticated request's open vault. No account
// password is needed, and PRF-less providers use server-managed key custody.
func (s *Service) persistVerifiedPasskey(ctx context.Context, userID, name string, prfResult, salt []byte, credential webauthn.Credential, now time.Time) (control.PasskeySummary, error) {
	key, err := s.sessionVaultKey(ctx, userID)
	if err != nil {
		return control.PasskeySummary{}, ErrInvalidCredentials
	}
	defer key.Destroy()
	dek := key[:]
	vaultID, err := s.store.LookupVaultID(ctx, userID)
	if err != nil {
		clear(dek)
		return control.PasskeySummary{}, err
	}
	defer clear(dek)
	input := control.PasskeyCredentialInput{
		UserID: userID, Name: name, Credential: credential, PRFSalt: salt,
		PasswordRequired: false,
	}
	if len(prfResult) != 0 {
		binding := keyenvelope.Context{UserID: userID, VaultID: vaultID}
		envelope, err := keyenvelope.WrapWithPasskey(dek, prfResult, binding)
		if err != nil {
			return control.PasskeySummary{}, err
		}
		verifiedDEK, err := keyenvelope.UnwrapWithPasskey(envelope, prfResult, binding)
		if err != nil || !bytes.Equal(verifiedDEK, dek) {
			clear(verifiedDEK)
			return control.PasskeySummary{}, ErrPasskeyPRFRequired
		}
		clear(verifiedDEK)
		input.VaultEnvelope = *envelope
	} else {
		envelope, err := keyenvelope.WrapWithServerPasskey(dek, s.passkeyCustodyKey[:], keyenvelope.Context{UserID: userID, VaultID: vaultID}, credential.ID)
		if err != nil {
			return control.PasskeySummary{}, err
		}
		input.VaultEnvelope = *envelope
	}
	record, err := s.passkeyStore.CreatePasskeyCredential(ctx, input, now)
	if err != nil {
		return control.PasskeySummary{}, err
	}
	return record.Summary(), nil
}

func (s *Service) BeginPasskeyLogin(ctx context.Context, email, clientKey string) (PasskeyLoginBegin, error) {
	if !s.passkeysReady() {
		return PasskeyLoginBegin{}, ErrPasskeysUnavailable
	}
	return s.beginPrivatePasskeyLogin(ctx, email, clientKey)
}

// BeginDiscoverablePasskeyLogin does not need an account name. Every newly
// registered passkey uses the same public PRF input, so the browser can return
// the vault-unwrapping result in the credential selection assertion itself.
func (s *Service) BeginDiscoverablePasskeyLogin(_ context.Context, clientKey string) (PasskeyLoginBegin, error) {
	if !s.passkeysReady() {
		return PasskeyLoginBegin{}, ErrPasskeysUnavailable
	}
	assertion, session, err := s.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(webauthn.WithExtensionPRF(protocol.PRFValues{
			First: protocol.URLEncodedBase64(discoverablePRFSalt[:]),
		})),
	)
	if err != nil {
		return PasskeyLoginBegin{}, fmt.Errorf("begin discoverable passkey login: %w", err)
	}
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: passkeyCeremonyDiscoverableLogin, ClientKey: clientKey, Session: *session,
	})
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	return PasskeyLoginBegin{CeremonyID: ceremonyID, Options: assertion}, nil
}

func (s *Service) FinishDiscoverablePasskeyLogin(ctx context.Context, input FinishPasskeyLoginInput, now time.Time) (*middleware.Session, error) {
	if !s.passkeysReady() {
		return nil, ErrPasskeysUnavailable
	}
	started := time.Now()
	session, err := s.finishDiscoverablePasskeyLogin(ctx, input, now)
	if err != nil {
		holdPasskeyLoginFloor(ctx, started)
	}
	return session, err
}

func (s *Service) finishDiscoverablePasskeyLogin(ctx context.Context, input FinishPasskeyLoginInput, now time.Time) (result *middleware.Session, err error) {
	ceremony, err := s.takePasskeyCeremony(input.CeremonyID, passkeyCeremonyDiscoverableLogin, input.ClientKey)
	if err != nil || (len(input.PRFResult) != 0 && len(input.PRFResult) != keyenvelope.PasskeySecretSize) {
		return nil, ErrInvalidCredentials
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input.CredentialJSON)
	if err != nil || len(parsed.Response.UserHandle) == 0 {
		return nil, ErrInvalidCredentials
	}
	userID := string(parsed.Response.UserHandle)
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	adapter, records, err := s.loadPasskeyUser(ctx, userID)
	if err != nil || adapter.user.State != control.UserActive {
		return nil, ErrInvalidCredentials
	}
	throttleKey := LoginThrottleKey(adapter.user.Email)
	if retryAfter, throttleErr := s.checkLoginThrottle(ctx, throttleKey, now); throttleErr != nil {
		return nil, throttleErr
	} else if retryAfter > 0 {
		return nil, &LoginThrottledError{RetryAfter: retryAfter}
	}
	passwordChecked := false
	defer func() {
		switch {
		case err == nil:
			s.clearLoginFailures(ctx, throttleKey)
		case errors.Is(err, ErrInvalidCredentials) && !passwordChecked:
			s.recordLoginFailure(ctx, throttleKey, now)
		}
	}()
	var expected *control.PasskeyCredential
	for index := range records {
		if bytes.Equal(records[index].ID, parsed.RawID) && bytes.Equal(records[index].PRFSalt, discoverablePRFSalt[:]) {
			expected = &records[index]
			break
		}
	}
	if expected == nil {
		return nil, ErrInvalidCredentials
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if !bytes.Equal(userHandle, []byte(userID)) || !bytes.Equal(rawID, expected.ID) {
			return nil, ErrInvalidCredentials
		}
		return adapter, nil
	}
	_, updated, err := s.webauthn.ValidatePasskeyLogin(handler, ceremony.Session, parsed)
	if err != nil || updated == nil || updated.Authenticator.CloneWarning || !bytes.Equal(updated.ID, expected.ID) {
		return nil, ErrInvalidCredentials
	}
	vaultID, err := s.store.LookupVaultID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var dek []byte
	if expected.VaultEnvelope.Kind == keyenvelope.KindServerPasskey {
		dek, err = keyenvelope.UnwrapWithServerPasskey(&expected.VaultEnvelope, s.passkeyCustodyKey[:],
			keyenvelope.Context{UserID: expected.UserID, VaultID: vaultID}, expected.ID)
	} else if len(input.PRFResult) == keyenvelope.PasskeySecretSize && !expected.PasswordRequired {
		dek, err = keyenvelope.UnwrapWithPasskey(&expected.VaultEnvelope, input.PRFResult,
			keyenvelope.Context{UserID: userID, VaultID: vaultID})
	} else if len(input.Password) != 0 {
		passwordChecked = true // unwrapVaultWithPassword accounts for password failures.
		dek, _, err = s.unwrapVaultWithPassword(ctx, userID, input.Password, now)
	} else {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		clear(dek)
		return nil, ErrInvalidCredentials
	}
	defer clear(dek)
	key, err := securedb.NewRawKey(dek)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	if err := s.passkeyStore.RecordSuccessfulPasskeyUse(ctx, *expected, *updated, now, true); err != nil {
		if errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	session, err := s.openSession(adapter.user, vaultID, &key)
	if err != nil {
		return nil, fmt.Errorf("open discoverable passkey vault session: %w", err)
	}
	if session == nil {
		return nil, errors.New("open discoverable passkey vault session returned no session")
	}
	return session, nil
}

func (s *Service) BeginPasskeyReauthentication(ctx context.Context, userID, clientKey string) (PasskeyLoginBegin, error) {
	if !s.passkeysReady() {
		return PasskeyLoginBegin{}, ErrPasskeysUnavailable
	}
	return s.beginPasskeyAssertion(ctx, userID, clientKey, passkeyCeremonyReauth)
}

func (s *Service) beginPasskeyAssertion(ctx context.Context, userID, clientKey, kind string) (PasskeyLoginBegin, error) {
	adapter, records, err := s.loadPasskeyUser(ctx, userID)
	if err != nil || len(records) == 0 {
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	if adapter.user.State != control.UserActive {
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	throttleKey := LoginThrottleKey(adapter.user.Email)
	if retryAfter, throttleErr := s.checkLoginThrottle(ctx, throttleKey, time.Now().UTC()); throttleErr != nil {
		return PasskeyLoginBegin{}, throttleErr
	} else if retryAfter > 0 {
		return PasskeyLoginBegin{}, &LoginThrottledError{RetryAfter: retryAfter}
	}
	evalByCredential := make(map[string]protocol.PRFValues, len(records))
	for _, record := range records {
		evalByCredential[base64.RawURLEncoding.EncodeToString(record.ID)] = protocol.PRFValues{
			First: protocol.URLEncodedBase64(record.PRFSalt),
		}
	}
	assertion, session, err := s.webauthn.BeginLogin(
		adapter,
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(webauthn.WithExtensionPRFByCredential(evalByCredential, nil)),
	)
	if err != nil {
		return PasskeyLoginBegin{}, fmt.Errorf("begin passkey login: %w", err)
	}
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: kind, UserID: userID, ClientKey: clientKey, Session: *session,
		ThrottleKey: throttleKey,
	})
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	return PasskeyLoginBegin{CeremonyID: ceremonyID, Options: assertion}, nil
}

// FinishPasskeyLogin completes a discoverable login. Every failure path holds
// the response for passkeyLoginResponseFloor so a decoy ceremony cannot be
// distinguished from a real account by response time. Successful logins are
// not delayed; producing one requires the credential.
func (s *Service) FinishPasskeyLogin(ctx context.Context, input FinishPasskeyLoginInput, now time.Time) (*middleware.Session, error) {
	if !s.passkeysReady() {
		return nil, ErrPasskeysUnavailable
	}
	started := time.Now()
	session, err := s.finishPasskeyLogin(ctx, input, now)
	if err != nil {
		holdPasskeyLoginFloor(ctx, started)
	}
	return session, err
}

func (s *Service) finishPasskeyLogin(ctx context.Context, input FinishPasskeyLoginInput, now time.Time) (*middleware.Session, error) {
	user, vaultID, dek, unlock, err := s.validatePasskeyAssertion(ctx, "", passkeyCeremonyLogin, input, now, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer clear(dek)
	key, err := securedb.NewRawKey(dek)
	if err != nil {
		return nil, err
	}
	defer key.Destroy()
	session, err := s.openSession(user, vaultID, &key)
	if err != nil {
		return nil, fmt.Errorf("open passkey authenticated vault session: %w", err)
	}
	if session == nil {
		return nil, errors.New("open passkey authenticated vault session returned no session")
	}
	return session, nil
}

func (s *Service) FinishPasskeyReauthentication(
	ctx context.Context,
	userID string,
	input FinishPasskeyLoginInput,
	now time.Time,
) error {
	if !s.passkeysReady() {
		return ErrPasskeysUnavailable
	}
	_, _, dek, unlock, err := s.validatePasskeyAssertion(ctx, userID, passkeyCeremonyReauth, input, now, false)
	if unlock != nil {
		defer unlock()
	}
	clear(dek)
	return err
}

func (s *Service) validatePasskeyAssertion(
	ctx context.Context,
	expectedUserID, kind string,
	input FinishPasskeyLoginInput,
	now time.Time,
	recordLogin bool,
) (summary control.UserSummary, resultVaultID string, resultDEK []byte, resultUnlock func(), err error) {
	ceremony, ceremonyErr := s.takePasskeyCeremony(input.CeremonyID, kind, input.ClientKey)
	if ceremonyErr != nil {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	passwordChecked := false
	if ceremony.ThrottleKey != "" {
		retryAfter, throttleErr := s.checkLoginThrottle(ctx, ceremony.ThrottleKey, now)
		if throttleErr != nil {
			return control.UserSummary{}, "", nil, nil, throttleErr
		}
		if retryAfter > 0 {
			return control.UserSummary{}, "", nil, nil, &LoginThrottledError{RetryAfter: retryAfter}
		}
		// The counter is shared with password authentication so the lockout
		// cannot be bypassed by switching between methods.
		defer func() {
			switch {
			case err == nil:
				s.clearLoginFailures(ctx, ceremony.ThrottleKey)
			case errors.Is(err, ErrInvalidCredentials) && !passwordChecked:
				s.recordLoginFailure(ctx, ceremony.ThrottleKey, now)
			}
		}()
	}
	if ceremony.UserID == "" || (len(input.PRFResult) != 0 && len(input.PRFResult) != keyenvelope.PasskeySecretSize) {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	if expectedUserID != "" && ceremony.UserID != expectedUserID {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input.CredentialJSON)
	if err != nil {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	unlock, err := s.lockAccount("user:" + ceremony.UserID)
	if err != nil {
		return control.UserSummary{}, "", nil, nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			unlock()
		}
	}()
	user, records, err := s.loadPasskeyUser(ctx, ceremony.UserID)
	if err != nil {
		return control.UserSummary{}, "", nil, nil, err
	}
	if user.user.State != control.UserActive {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	updated, err := s.webauthn.ValidateLogin(user, ceremony.Session, parsed)
	if err != nil || updated == nil || updated.Authenticator.CloneWarning {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	var expected *control.PasskeyCredential
	for index := range records {
		if bytes.Equal(records[index].ID, updated.ID) {
			expected = &records[index]
			break
		}
	}
	if expected == nil {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	vaultID, err := s.store.LookupVaultID(ctx, ceremony.UserID)
	if err != nil {
		return control.UserSummary{}, "", nil, nil, err
	}
	var dek []byte
	if kind == passkeyCeremonyReauth {
		dek = nil
	} else if expected.VaultEnvelope.Kind == keyenvelope.KindServerPasskey {
		dek, err = keyenvelope.UnwrapWithServerPasskey(&expected.VaultEnvelope, s.passkeyCustodyKey[:],
			keyenvelope.Context{UserID: expected.UserID, VaultID: vaultID}, expected.ID)
	} else if len(input.PRFResult) == keyenvelope.PasskeySecretSize && !expected.PasswordRequired {
		dek, err = keyenvelope.UnwrapWithPasskey(
			&expected.VaultEnvelope, input.PRFResult,
			keyenvelope.Context{UserID: ceremony.UserID, VaultID: vaultID},
		)
	} else if len(input.Password) != 0 {
		passwordChecked = true // unwrapVaultWithPassword accounts for password failures.
		dek, _, err = s.unwrapVaultWithPassword(ctx, ceremony.UserID, input.Password, now)
	} else {
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	if err != nil {
		clear(dek)
		return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
	}
	if err := s.passkeyStore.RecordSuccessfulPasskeyUse(ctx, *expected, *updated, now, recordLogin); err != nil {
		clear(dek)
		if errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden) {
			return control.UserSummary{}, "", nil, nil, ErrInvalidCredentials
		}
		return control.UserSummary{}, "", nil, nil, err
	}
	succeeded = true
	return user.user, vaultID, dek, unlock, nil
}

func (s *Service) ListPasskeys(ctx context.Context, userID string) ([]control.PasskeySummary, error) {
	if !s.passkeysReady() {
		return nil, ErrPasskeysUnavailable
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	key, err := s.sessionVaultKey(ctx, userID)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	defer key.Destroy()
	vaultID, err := s.store.LookupVaultID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.upgradePasskeyEnvelopes(ctx, userID, vaultID, key[:], time.Now().UTC()); err != nil {
		return nil, err
	}
	records, err := s.passkeyStore.ListPasskeyCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]control.PasskeySummary, 0, len(records))
	for _, record := range records {
		result = append(result, record.Summary())
	}
	return result, nil
}

func (s *Service) DeletePasskey(ctx context.Context, userID string, credentialID []byte) error {
	if !s.passkeysReady() {
		return ErrPasskeysUnavailable
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return err
	}
	if err := s.passkeyStore.DeletePasskeyCredential(ctx, userID, credentialID); err != nil {
		unlock()
		return err
	}
	s.sessions.DeleteAllSessionsForUser(userID)
	waitForDrain, _ := s.vaults.BeginUserDrain(userID)
	unlock()
	s.finishDrain(ctx, waitForDrain)
	return nil
}

func (s *Service) DeleteAllPasskeys(ctx context.Context, userID string) (int, error) {
	if !s.passkeysReady() {
		return 0, ErrPasskeysUnavailable
	}
	unlock, err := s.lockAccount("user:" + userID)
	if err != nil {
		return 0, err
	}
	removed, err := s.passkeyStore.DeleteAllPasskeyCredentials(ctx, userID)
	if err != nil {
		unlock()
		return 0, err
	}
	s.sessions.DeleteAllSessionsForUser(userID)
	waitForDrain, _ := s.vaults.BeginUserDrain(userID)
	unlock()
	s.finishDrain(ctx, waitForDrain)
	return removed, nil
}

func (s *Service) unwrapVaultWithPassword(ctx context.Context, userID string, password []byte, now time.Time) ([]byte, string, error) {
	user, userErr := s.store.GetUser(ctx, userID)
	throttleKey := ""
	if userErr == nil {
		throttleKey = LoginThrottleKey(user.Email)
		if retryAfter, throttleErr := s.checkLoginThrottle(ctx, throttleKey, now); throttleErr != nil {
			return nil, "", throttleErr
		} else if retryAfter > 0 {
			return nil, "", &LoginThrottledError{RetryAfter: retryAfter}
		}
	}
	if validateNewPassword(password) != nil {
		if err := s.runDummyPassword(ctx, password); err != nil {
			return nil, "", err
		}
		if throttleKey != "" {
			s.recordLoginFailure(ctx, throttleKey, now)
		}
		return nil, "", ErrInvalidCredentials
	}
	if userErr != nil || user.State != control.UserActive {
		if dummyErr := s.runDummyPassword(ctx, password); dummyErr != nil {
			return nil, "", dummyErr
		}
		if userErr != nil && !errors.Is(userErr, control.ErrNotFound) {
			return nil, "", userErr
		}
		if throttleKey != "" {
			s.recordLoginFailure(ctx, throttleKey, now)
		}
		return nil, "", ErrInvalidCredentials
	}
	credential, err := s.store.GetPasswordCredential(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	vaultID, err := s.store.LookupVaultID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	release, err := s.reserveKDF(ctx)
	if err != nil {
		return nil, "", err
	}
	dek, unwrapErr := keyenvelope.UnwrapWithPassword(
		&credential.Envelope, password, keyenvelope.Context{UserID: userID, VaultID: vaultID},
	)
	release()
	if unwrapErr != nil {
		clear(dek)
		if errors.Is(unwrapErr, keyenvelope.ErrAuthentication) {
			s.recordLoginFailure(ctx, throttleKey, now)
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", unwrapErr
	}
	s.clearLoginFailures(ctx, throttleKey)
	return dek, vaultID, nil
}

func (s *Service) loadPasskeyUser(ctx context.Context, userID string) (webAuthnUser, []control.PasskeyCredential, error) {
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return webAuthnUser{}, nil, err
	}
	records, err := s.passkeyStore.ListPasskeyCredentials(ctx, userID)
	if err != nil {
		return webAuthnUser{}, nil, err
	}
	credentials := make([]webauthn.Credential, len(records))
	for index := range records {
		credentials[index] = records[index].Credential
	}
	return webAuthnUser{user: user, credentials: credentials}, records, nil
}

func (s *Service) passkeysReady() bool {
	return s != nil && s.ready() && s.passkeyStore != nil && s.webauthn != nil && s.ceremonies != nil
}

func (s *Service) storePasskeyCeremony(ceremony passkeyCeremony) (string, error) {
	idBytes, err := randomPasskeyBytes(32)
	if err != nil {
		clear(ceremony.PRFSalt)
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	clear(idBytes)
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	now := time.Now()
	for key, existing := range s.ceremonies {
		if existing.Session.Expires.Before(now) {
			clear(existing.PRFSalt)
			delete(s.ceremonies, key)
		}
	}
	if len(s.ceremonies) >= maxPasskeyCeremonies {
		clear(ceremony.PRFSalt)
		return "", ErrAuthenticationBusy
	}
	ceremony.PRFSalt = append([]byte(nil), ceremony.PRFSalt...)
	s.ceremonies[id] = ceremony
	return id, nil
}

func (s *Service) takePasskeyCeremony(id, kind, clientKey string) (passkeyCeremony, error) {
	if len(id) != 43 || strings.TrimSpace(clientKey) == "" {
		return passkeyCeremony{}, ErrPasskeyCeremony
	}
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	ceremony, ok := s.ceremonies[id]
	if ok {
		delete(s.ceremonies, id)
	}
	if !ok || ceremony.Kind != kind || ceremony.ClientKey != clientKey || ceremony.Session.Expires.Before(time.Now()) {
		clear(ceremony.PRFSalt)
		return passkeyCeremony{}, ErrPasskeyCeremony
	}
	return ceremony, nil
}

func randomPasskeyBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		clear(value)
		return nil, fmt.Errorf("generate passkey ceremony secret: %w", err)
	}
	return value, nil
}

type passkeyEnvelopeUpgrader interface {
	ReplacePasskeyEnvelope(context.Context, control.PasskeyCredential, keyenvelope.Envelope, time.Time) error
}

// Called only with a proven plaintext DEK under the account lifecycle lock.
func (s *Service) upgradePasskeyEnvelopes(ctx context.Context, userID, vaultID string, dek []byte, now time.Time) error {
	upgrader, ok := s.passkeyStore.(passkeyEnvelopeUpgrader)
	if !ok || s.webauthn == nil {
		return nil
	}
	records, err := s.passkeyStore.ListPasskeyCredentials(ctx, userID)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.VaultEnvelope.Kind == keyenvelope.KindServerPasskey {
			continue
		}
		envelope, err := keyenvelope.WrapWithServerPasskey(dek, s.passkeyCustodyKey[:], keyenvelope.Context{UserID: userID, VaultID: vaultID}, record.ID)
		if err != nil {
			return err
		}
		if err := upgrader.ReplacePasskeyEnvelope(ctx, record, *envelope, now); err != nil {
			return err
		}
	}
	return nil
}

func passkeySessionID(ctx context.Context) string {
	if session, ok := middleware.SessionFromContext(ctx); ok {
		return session.ID
	}
	return ""
}
