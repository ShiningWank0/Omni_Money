package serverauth

import (
	"bytes"
	"context"
	"crypto/rand"
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
	passkeyCeremonyRegistration = "registration"
	passkeyCeremonyLogin        = "login"
	passkeyCeremonyReauth       = "reauthentication"
	maxPasskeyCeremonies        = 4096
)

var (
	ErrPasskeysUnavailable = errors.New("passkey authentication is not configured")
	ErrPasskeyPRFRequired  = errors.New("this passkey does not support the WebAuthn PRF extension")
	ErrPasskeyCeremony     = errors.New("passkey ceremony is invalid or expired")
)

type passkeyCeremony struct {
	Kind      string
	UserID    string
	ClientKey string
	Session   webauthn.SessionData
	PRFSalt   []byte
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

type FinishPasskeyLoginInput struct {
	CeremonyID     string
	ClientKey      string
	CredentialJSON json.RawMessage
	PRFResult      []byte
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
	prfSalt, err := randomPasskeyBytes(keyenvelope.PasskeySecretSize)
	if err != nil {
		return PasskeyRegistrationBegin{}, err
	}
	creation, session, err := s.webauthn.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExtensions(protocol.AuthenticationExtensions{
			"prf": map[string]any{"eval": map[string]any{"first": protocol.URLEncodedBase64(prfSalt)}},
		}),
	)
	if err != nil {
		clear(prfSalt)
		return PasskeyRegistrationBegin{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	_ = records // loadPasskeyUser already supplied exclusions through the adapter.
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: passkeyCeremonyRegistration, UserID: userID, ClientKey: clientKey,
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
	if err != nil || ceremony.UserID != userID {
		return control.PasskeySummary{}, ErrPasskeyCeremony
	}
	defer clear(ceremony.PRFSalt)
	if len(input.PRFResult) != keyenvelope.PasskeySecretSize {
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
	dek, vaultID, err := s.unwrapVaultWithPassword(ctx, userID, input.Password)
	if err != nil {
		clear(dek)
		return control.PasskeySummary{}, err
	}
	defer clear(dek)
	binding := keyenvelope.Context{UserID: userID, VaultID: vaultID}
	passkeyEnvelope, err := keyenvelope.WrapWithPasskey(dek, input.PRFResult, binding)
	if err != nil {
		return control.PasskeySummary{}, err
	}
	// Verify the newly persisted unlock path before committing the credential.
	verifiedDEK, err := keyenvelope.UnwrapWithPasskey(passkeyEnvelope, input.PRFResult, binding)
	if err != nil || !bytes.Equal(verifiedDEK, dek) {
		clear(verifiedDEK)
		return control.PasskeySummary{}, ErrPasskeyPRFRequired
	}
	clear(verifiedDEK)
	record, err := s.passkeyStore.CreatePasskeyCredential(ctx, control.PasskeyCredentialInput{
		UserID: userID, Name: input.Name, Credential: *credential,
		PRFSalt: ceremony.PRFSalt, VaultEnvelope: *passkeyEnvelope,
	}, now)
	if err != nil {
		return control.PasskeySummary{}, err
	}
	return record.Summary(), nil
}

func (s *Service) BeginPasskeyLogin(ctx context.Context, email, clientKey string) (PasskeyLoginBegin, error) {
	if !s.passkeysReady() {
		return PasskeyLoginBegin{}, ErrPasskeysUnavailable
	}
	email, err := normalizeLoginEmail(email)
	if err != nil {
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || user.State != control.UserActive {
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	return s.beginPasskeyAssertion(ctx, user.ID, clientKey, passkeyCeremonyLogin)
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
	evalByCredential := make(map[string]any, len(records))
	for _, record := range records {
		evalByCredential[base64.RawURLEncoding.EncodeToString(record.ID)] = map[string]any{
			"first": protocol.URLEncodedBase64(record.PRFSalt),
		}
	}
	assertion, session, err := s.webauthn.BeginLogin(
		adapter,
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(protocol.AuthenticationExtensions{
			"prf": map[string]any{"evalByCredential": evalByCredential},
		}),
	)
	if err != nil {
		return PasskeyLoginBegin{}, fmt.Errorf("begin passkey login: %w", err)
	}
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: kind, UserID: userID, ClientKey: clientKey, Session: *session,
	})
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	return PasskeyLoginBegin{CeremonyID: ceremonyID, Options: assertion}, nil
}

func (s *Service) FinishPasskeyLogin(ctx context.Context, input FinishPasskeyLoginInput, now time.Time) (*middleware.Session, error) {
	if !s.passkeysReady() {
		return nil, ErrPasskeysUnavailable
	}
	user, vaultID, dek, err := s.validatePasskeyAssertion(ctx, "", passkeyCeremonyLogin, input, now, true)
	if err != nil {
		return nil, err
	}
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
	_, _, dek, err := s.validatePasskeyAssertion(ctx, userID, passkeyCeremonyReauth, input, now, false)
	clear(dek)
	return err
}

func (s *Service) validatePasskeyAssertion(
	ctx context.Context,
	expectedUserID, kind string,
	input FinishPasskeyLoginInput,
	now time.Time,
	recordLogin bool,
) (control.UserSummary, string, []byte, error) {
	ceremony, err := s.takePasskeyCeremony(input.CeremonyID, kind, input.ClientKey)
	if err != nil || len(input.PRFResult) != keyenvelope.PasskeySecretSize {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	if expectedUserID != "" && ceremony.UserID != expectedUserID {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input.CredentialJSON)
	if err != nil {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	unlock, err := s.lockAccount("user:" + ceremony.UserID)
	if err != nil {
		return control.UserSummary{}, "", nil, err
	}
	defer unlock()
	user, records, err := s.loadPasskeyUser(ctx, ceremony.UserID)
	if err != nil || user.user.State != control.UserActive {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	updated, err := s.webauthn.ValidateLogin(user, ceremony.Session, parsed)
	if err != nil || updated == nil || updated.Authenticator.CloneWarning {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	var expected *control.PasskeyCredential
	for index := range records {
		if bytes.Equal(records[index].ID, updated.ID) {
			expected = &records[index]
			break
		}
	}
	if expected == nil {
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	vaultID, err := s.store.LookupVaultID(ctx, ceremony.UserID)
	if err != nil {
		return control.UserSummary{}, "", nil, err
	}
	dek, err := keyenvelope.UnwrapWithPasskey(
		&expected.VaultEnvelope, input.PRFResult,
		keyenvelope.Context{UserID: ceremony.UserID, VaultID: vaultID},
	)
	if err != nil {
		clear(dek)
		return control.UserSummary{}, "", nil, ErrInvalidCredentials
	}
	if err := s.passkeyStore.RecordSuccessfulPasskeyUse(ctx, *expected, *updated, now, recordLogin); err != nil {
		clear(dek)
		if errors.Is(err, control.ErrCredentialConflict) || errors.Is(err, control.ErrForbidden) {
			return control.UserSummary{}, "", nil, ErrInvalidCredentials
		}
		return control.UserSummary{}, "", nil, err
	}
	return user.user, vaultID, dek, nil
}

func (s *Service) ListPasskeys(ctx context.Context, userID string) ([]control.PasskeySummary, error) {
	if !s.passkeysReady() {
		return nil, ErrPasskeysUnavailable
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
	defer unlock()
	return s.passkeyStore.DeletePasskeyCredential(ctx, userID, credentialID)
}

func (s *Service) unwrapVaultWithPassword(ctx context.Context, userID string, password []byte) ([]byte, string, error) {
	if validateNewPassword(password) != nil {
		if err := s.runDummyPassword(ctx, password); err != nil {
			return nil, "", err
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
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", unwrapErr
	}
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
