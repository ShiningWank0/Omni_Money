package serverauth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

const registrationTestPassword = "correct horse battery staple"

type registrationTestStore struct {
	*fakeControlStore
	records []control.PasskeyCredential
	created []control.PasskeyCredentialInput
}

func (s *registrationTestStore) CreatePasskeyCredential(_ context.Context, input control.PasskeyCredentialInput, now time.Time) (control.PasskeyCredential, error) {
	record := control.PasskeyCredential{
		ID: input.Credential.ID, UserID: input.UserID, Name: input.Name,
		Credential: input.Credential, PRFSalt: append([]byte(nil), input.PRFSalt...),
		VaultEnvelope: input.VaultEnvelope, PasswordRequired: input.PasswordRequired, CreatedAt: now,
	}
	s.records = append(s.records, record)
	s.created = append(s.created, input)
	return record, nil
}

func (s *registrationTestStore) ListPasskeyCredentials(_ context.Context, userID string) ([]control.PasskeyCredential, error) {
	result := make([]control.PasskeyCredential, 0, len(s.records))
	for _, record := range s.records {
		if record.UserID == userID {
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *registrationTestStore) GetPasskeyCredential(_ context.Context, userID string, id []byte) (control.PasskeyCredential, error) {
	for _, record := range s.records {
		if record.UserID == userID && bytes.Equal(record.ID, id) {
			return record, nil
		}
	}
	return control.PasskeyCredential{}, control.ErrNotFound
}

func (s *registrationTestStore) RecordSuccessfulPasskeyUse(_ context.Context, expected control.PasskeyCredential, updated webauthn.Credential, _ time.Time, _ bool) error {
	for index, record := range s.records {
		if bytes.Equal(record.ID, expected.ID) {
			if record.Credential.Authenticator.SignCount != expected.Credential.Authenticator.SignCount {
				return errors.New("stale credential commit")
			}
			s.records[index].Credential = updated
			return nil
		}
	}
	return errors.New("unexpected credential commit")
}

func (s *registrationTestStore) DeletePasskeyCredential(_ context.Context, userID string, id []byte) error {
	for index, record := range s.records {
		if record.UserID == userID && bytes.Equal(record.ID, id) {
			s.records = append(s.records[:index], s.records[index+1:]...)
			return nil
		}
	}
	return control.ErrNotFound
}

func (s *registrationTestStore) DeleteAllPasskeyCredentials(_ context.Context, userID string) (int, error) {
	kept := s.records[:0]
	removed := 0
	for _, record := range s.records {
		if record.UserID == userID {
			removed++
			continue
		}
		kept = append(kept, record)
	}
	s.records = kept
	return removed, nil
}

func newRegistrationTestService(t *testing.T, dek []byte) (*Service, *registrationTestStore) {
	t.Helper()
	user := control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", DisplayName: "Person", State: control.UserActive}
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	passwordEnvelope, err := keyenvelope.WrapWithPassword(dek, []byte(registrationTestPassword), binding)
	if err != nil {
		t.Fatal(err)
	}
	store := &registrationTestStore{fakeControlStore: &fakeControlStore{}}
	store.getUserByEmailFn = func(_ context.Context, email string) (control.UserSummary, error) {
		if email != user.Email {
			return control.UserSummary{}, control.ErrNotFound
		}
		return user, nil
	}
	store.getUserFn = func(_ context.Context, id string) (control.UserSummary, error) {
		if id != user.ID {
			return control.UserSummary{}, control.ErrNotFound
		}
		return user, nil
	}
	store.lookupVaultIDFn = func(context.Context, string) (string, error) { return serverAuthTestVaultID, nil }
	store.getPasswordCredentialFn = func(context.Context, string) (control.PasswordCredential, error) {
		return control.PasswordCredential{UserID: user.ID, Envelope: *passwordEnvelope}, nil
	}
	verifier, err := webauthn.New(&webauthn.Config{
		RPID: "money.example.test", RPDisplayName: "Omni Money", RPOrigins: []string{"https://money.example.test"},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute, TimeoutUVD: 5 * time.Minute},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute, TimeoutUVD: 5 * time.Minute},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		Store: store, WebAuthn: verifier, PasskeyPrivacyKey: bytes.Repeat([]byte{71}, 32),
		Sessions: &fakeSessionInvalidator{}, Vaults: &fakeVaultDrainer{},
		OpenSession: func(user control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
			defer key.Destroy()
			if user.ID != serverAuthTestUserID || vaultID != serverAuthTestVaultID || !bytes.Equal(key[:], dek) {
				return nil, errors.New("login opened the wrong vault or used the wrong key")
			}
			return &middleware.Session{ID: "registration-session", UserID: user.ID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

// signedRegistrationAttestation builds a "none" attestation response whose
// client extension results announce PRF support, optionally with a create-time
// PRF result. Passing a nil prfResult produces the response that made the
// Bitwarden browser extension unregistrable.
func signedRegistrationAttestation(t *testing.T, begin PasskeyRegistrationBegin, key *ecdsa.PrivateKey, credentialID []byte, origin string, uv bool, prfResult []byte) json.RawMessage {
	t.Helper()
	client, err := json.Marshal(map[string]any{
		"type": "webauthn.create", "challenge": begin.Options.Response.Challenge.String(), "origin": origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte("money.example.test"))
	flags := byte(0x01) | 0x40
	if uv {
		flags |= 0x04
	}
	coseKey, err := cbor.Marshal(map[int]any{
		1: 2, 3: -7, -1: 1,
		-2: key.X.FillBytes(make([]byte, 32)),
		-3: key.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 0, 18+len(credentialID)+len(coseKey))
	attested = append(attested, make([]byte, 16)...)
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(credentialID)))
	attested = append(attested, credentialID...)
	attested = append(attested, coseKey...)
	auth := make([]byte, 0, 37+len(attested))
	auth = append(auth, rp[:]...)
	auth = append(auth, flags)
	auth = binary.BigEndian.AppendUint32(auth, 0)
	auth = append(auth, attested...)
	attestationObject, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		t.Fatal(err)
	}
	prf := map[string]any{"enabled": true}
	if prfResult != nil {
		prf["results"] = map[string]any{"first": base64.RawURLEncoding.EncodeToString(prfResult)}
	}
	encode := base64.RawURLEncoding.EncodeToString
	response, err := json.Marshal(map[string]any{
		"id": encode(credentialID), "rawId": encode(credentialID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": encode(client), "attestationObject": encode(attestationObject), "transports": []string{},
		},
		"clientExtensionResults": map[string]any{"prf": prf},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func signedRegistrationAttestationWithoutPRF(t *testing.T, begin PasskeyRegistrationBegin, key *ecdsa.PrivateKey, credentialID []byte, origin string) json.RawMessage {
	t.Helper()
	response := signedRegistrationAttestation(t, begin, key, credentialID, origin, true, nil)
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		t.Fatal(err)
	}
	payload["clientExtensionResults"] = map[string]any{}
	response, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func registrationPRFSalt(t *testing.T, service *Service, begin PasskeyRegistrationBegin) []byte {
	t.Helper()
	prf := browserPRFInputs(t, begin.Options.Response.Extensions)
	if len(prf.Eval) != 0 || len(prf.EvalByCredential) != 0 {
		t.Fatal("registration must probe PRF without evaluating at creation")
	}
	return bytes.Clone(service.ceremonies[begin.CeremonyID].PRFSalt)
}

func beginRegistrationAssertion(t *testing.T, service *Service, begin PasskeyRegistrationBegin, attestation json.RawMessage) PasskeyLoginBegin {
	t.Helper()
	result, err := service.BeginPasskeyRegistrationAssertion(context.Background(), serverAuthTestUserID, BeginPasskeyRegistrationAssertionInput{
		CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: attestation,
	})
	if err != nil {
		t.Fatalf("begin registration assertion: %v", err)
	}
	return result
}

func TestPasskeyRegistrationWithCreateTimePRFStillPersists(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x11}, 32)
	prfResult := bytes.Repeat([]byte{0x22}, keyenvelope.PasskeySecretSize)
	attestation := signedRegistrationAttestation(t, begin, key, credentialID, "https://money.example.test", true, prfResult)
	if _, err := service.FinishPasskeyRegistration(ctx, serverAuthTestUserID, FinishPasskeyRegistrationInput{
		CeremonyID: begin.CeremonyID, ClientKey: "client", Name: "Old path",
		Password: []byte(registrationTestPassword), CredentialJSON: attestation, PRFResult: prfResult,
	}, serverAuthTestNow); err != nil {
		t.Fatalf("create-time PRF registration failed: %v", err)
	}
	if len(store.records) != 1 || !bytes.Equal(store.records[0].PRFSalt, discoverablePRFSalt[:]) {
		t.Fatal("create-time PRF registration did not persist the original salt")
	}
}

func TestPasskeyWithoutPRFRegistersAndRequiresPasswordAfterAssertion(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x71}, 32)
	attestation := signedRegistrationAttestationWithoutPRF(t, begin, key, credentialID, "https://money.example.test")
	summary, err := service.FinishPasskeyRegistration(ctx, serverAuthTestUserID, FinishPasskeyRegistrationInput{
		CeremonyID: begin.CeremonyID, ClientKey: "client", Name: "Bitwarden",
		Password: []byte(registrationTestPassword), CredentialJSON: attestation,
	}, serverAuthTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.PasswordRequired || len(store.records) != 1 || !store.records[0].PasswordRequired {
		t.Fatal("PRF-less passkey was not registered for password unlock")
	}
	login, err := service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	assertion := signedPrivacyAssertion(t, login, store.records[0], key, "https://money.example.test", true)
	if session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: login.CeremonyID, ClientKey: "client", CredentialJSON: assertion,
	}, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) || session != nil {
		t.Fatalf("passwordless unlock of PRF-less credential: session=%v err=%v", session, err)
	}
	if err := store.ClearLoginFailures(ctx, LoginThrottleKey("person@example.test")); err != nil {
		t.Fatal(err)
	}
	login, err = service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	assertion = signedPrivacyAssertion(t, login, store.records[0], key, "https://money.example.test", true)
	if session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: login.CeremonyID, ClientKey: "client", CredentialJSON: assertion,
		Password: []byte("wrong password"),
	}, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) || session != nil {
		t.Fatalf("wrong password opened vault: session=%v err=%v", session, err)
	}
	store.throttleMu.Lock()
	failures := store.throttle[LoginThrottleKey("person@example.test")].failures
	store.throttleMu.Unlock()
	if failures != 1 {
		t.Fatalf("one rejected password counted as %d login failures", failures)
	}
	login, err = service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	assertion = signedPrivacyAssertion(t, login, store.records[0], key, "https://money.example.test", true)
	session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: login.CeremonyID, ClientKey: "client", CredentialJSON: assertion,
		Password: []byte(registrationTestPassword),
	}, serverAuthTestNow)
	if err != nil || session == nil {
		t.Fatalf("signed passkey plus password failed: session=%v err=%v", session, err)
	}
}

func TestNewPasskeyLogsInWithoutEmailWithOneAssertion(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	registration, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registrationPRFSalt(t, service, registration), discoverablePRFSalt[:]) {
		t.Fatal("registration did not use the discoverable PRF input")
	}
	if registration.Options.Response.AuthenticatorSelection.ResidentKey != protocol.ResidentKeyRequirementRequired {
		t.Fatal("registration did not require a discoverable credential")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x23}, 32)
	prfResult := bytes.Repeat([]byte{0x27}, keyenvelope.PasskeySecretSize)
	attestation := signedRegistrationAttestation(t, registration, key, credentialID, "https://money.example.test", true, prfResult)
	if _, err := service.FinishPasskeyRegistration(ctx, serverAuthTestUserID, FinishPasskeyRegistrationInput{
		CeremonyID: registration.CeremonyID, ClientKey: "client", Name: "Discoverable",
		Password: []byte(registrationTestPassword), CredentialJSON: attestation, PRFResult: prfResult,
	}, serverAuthTestNow); err != nil {
		t.Fatal(err)
	}
	wrongBegin, err := service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	wrongAssertion := signedPrivacyAssertion(t, wrongBegin, store.records[0], key, "https://money.example.test", true)
	if session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: wrongBegin.CeremonyID, ClientKey: "client", CredentialJSON: wrongAssertion,
		PRFResult: bytes.Repeat([]byte{0x28}, keyenvelope.PasskeySecretSize),
	}, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) || session != nil {
		t.Fatalf("wrong PRF opened the vault: session=%v, err=%v", session, err)
	}
	if store.records[0].Credential.Authenticator.SignCount != 0 {
		t.Fatal("failed login advanced the signature counter")
	}

	begin, err := service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	if len(begin.Options.Response.AllowedCredentials) != 0 || begin.Options.Response.UserVerification != protocol.VerificationRequired {
		t.Fatal("discoverable login must not require an email or credential allowlist")
	}
	prf := browserPRFInputs(t, begin.Options.Response.Extensions)
	salt, err := base64.RawURLEncoding.DecodeString(prf.Eval["first"])
	if err != nil || len(prf.EvalByCredential) != 0 || !bytes.Equal(salt, discoverablePRFSalt[:]) {
		t.Fatal("discoverable login did not evaluate the registration PRF input")
	}
	assertion := signedPrivacyAssertion(t, begin, store.records[0], key, "https://money.example.test", true)
	session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: assertion, PRFResult: prfResult,
	}, serverAuthTestNow)
	if err != nil || session == nil || session.UserID != serverAuthTestUserID {
		t.Fatalf("single-assertion login failed: session=%v, err=%v", session, err)
	}
	if store.records[0].Credential.Authenticator.SignCount != 1 {
		t.Fatal("single-assertion login did not persist the signature counter")
	}
	replayBegin, err := service.BeginDiscoverablePasskeyLogin(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	replayAssertion := signedPrivacyAssertion(t, replayBegin, store.records[0], key, "https://money.example.test", true)
	if replaySession, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: replayBegin.CeremonyID, ClientKey: "client", CredentialJSON: replayAssertion, PRFResult: prfResult,
	}, serverAuthTestNow); !errors.Is(err, ErrInvalidCredentials) || replaySession != nil {
		t.Fatalf("replayed counter opened the vault: session=%v, err=%v", replaySession, err)
	}
}

func TestPasskeyRegistrationAssertionWithoutCreateTimePRF(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	registrationSalt := registrationPRFSalt(t, service, begin)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x5a}, 32)
	attestation := signedRegistrationAttestation(t, begin, key, credentialID, "https://money.example.test", true, nil)
	assertBegin := beginRegistrationAssertion(t, service, begin, attestation)

	if len(assertBegin.Options.Response.AllowedCredentials) != 1 ||
		!bytes.Equal(assertBegin.Options.Response.AllowedCredentials[0].CredentialID, credentialID) ||
		assertBegin.Options.Response.UserVerification != protocol.VerificationRequired {
		t.Fatal("assertion does not target the candidate credential with required user verification")
	}
	if bytes.Equal(assertBegin.Options.Response.Challenge, begin.Options.Response.Challenge) {
		t.Fatal("registration assertion reused the creation challenge")
	}
	prf := browserPRFInputs(t, assertBegin.Options.Response.Extensions)
	salt, err := base64.RawURLEncoding.DecodeString(prf.Eval["first"])
	if err != nil || len(prf.EvalByCredential) != 0 || !bytes.Equal(salt, registrationSalt) {
		t.Fatal("assertion did not reuse the registration PRF salt")
	}

	prfResult := bytes.Repeat([]byte{0x2b}, keyenvelope.PasskeySecretSize)
	assertion := signedPrivacyAssertion(t, assertBegin, control.PasskeyCredential{ID: credentialID}, key, "https://money.example.test", true)
	summary, err := service.FinishPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, FinishPasskeyRegistrationAssertionInput{
		CeremonyID: assertBegin.CeremonyID, ClientKey: "client", Name: "Bitwarden",
		Password: []byte(registrationTestPassword), CredentialJSON: assertion, PRFResult: prfResult,
	}, serverAuthTestNow)
	if err != nil {
		t.Fatalf("finish registration assertion: %v", err)
	}
	if summary.Name != "Bitwarden" || len(store.records) != 1 {
		t.Fatalf("credential was not persisted: %+v", summary)
	}
	record := store.records[0]
	if !bytes.Equal(record.PRFSalt, registrationSalt) || !bytes.Equal(record.ID, credentialID) {
		t.Fatal("persisted credential lost its registration salt or ID")
	}
	if record.Credential.Authenticator.SignCount != 1 {
		t.Fatalf("registration assertion counter was not persisted: %d", record.Credential.Authenticator.SignCount)
	}
	unwrapped, err := keyenvelope.UnwrapWithPasskey(&record.VaultEnvelope, prfResult, keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID})
	if err != nil || !bytes.Equal(unwrapped, dek) {
		clear(unwrapped)
		t.Fatalf("persisted envelope does not unwrap with the PRF result: %v", err)
	}
	clear(unwrapped)

	// Replaying the registration assertion's counter must not open the vault.
	replayBegin, err := service.BeginPasskeyLogin(ctx, "person@example.test", "client")
	if err != nil {
		t.Fatal(err)
	}
	replayAssertion := signedPrivacyAssertionWithCount(t, replayBegin, record, key, "https://money.example.test", true, 1)
	replaySession, err := service.FinishPasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: replayBegin.CeremonyID, ClientKey: "client", CredentialJSON: replayAssertion, PRFResult: append([]byte(nil), prfResult...),
	}, serverAuthTestNow)
	if !errors.Is(err, ErrInvalidCredentials) || replaySession != nil {
		t.Fatalf("replayed registration counter was accepted: session=%v, err=%v", replaySession, err)
	}

	// The next counter must still decrypt the vault with the same PRF output.
	loginBegin, err := service.BeginPasskeyLogin(ctx, "person@example.test", "client")
	if err != nil {
		t.Fatal(err)
	}
	loginAssertion := signedPrivacyAssertionWithCount(t, loginBegin, record, key, "https://money.example.test", true, 2)
	session, err := service.FinishPasskeyLogin(ctx, FinishPasskeyLoginInput{
		CeremonyID: loginBegin.CeremonyID, ClientKey: "client", CredentialJSON: loginAssertion, PRFResult: append([]byte(nil), prfResult...),
	}, serverAuthTestNow)
	if err != nil || session == nil || session.UserID != serverAuthTestUserID {
		t.Fatalf("login after assertion registration failed: %v", err)
	}
	if got := store.records[0].Credential.Authenticator.SignCount; got != 2 {
		t.Fatalf("login assertion counter was not persisted: %d", got)
	}
}

func TestPasskeyRegistrationAssertionRejectsInvalidAttestations(t *testing.T) {
	for _, tc := range []string{"missing-create", "wrong-origin", "no-uv"} {
		t.Run(tc, func(t *testing.T) {
			dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
			service, store := newRegistrationTestService(t, dek)
			ctx := context.Background()
			begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
			if err != nil {
				t.Fatal(err)
			}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			credentialID := bytes.Repeat([]byte{0x5b}, 32)
			origin := "https://money.example.test"
			if tc == "wrong-origin" {
				origin = "https://attacker.example"
			}
			var attestation json.RawMessage
			if tc != "missing-create" {
				attestation = signedRegistrationAttestation(t, begin, key, credentialID, origin, tc != "no-uv", nil)
			}
			if _, err := service.BeginPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, BeginPasskeyRegistrationAssertionInput{
				CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: attestation,
			}); err == nil {
				t.Fatal("invalid attestation continued the registration")
			}
			if len(store.created) != 0 {
				t.Fatal("invalid attestation persisted a credential")
			}
		})
	}
}

func TestPasskeyRegistrationAssertionRejectsInvalidAssertions(t *testing.T) {
	for _, tc := range []string{"wrong-origin", "wrong-challenge", "no-uv", "wrong-signature", "foreign-credential", "wrong-client", "expired"} {
		t.Run(tc, func(t *testing.T) {
			dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
			service, store := newRegistrationTestService(t, dek)
			ctx := context.Background()
			begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
			if err != nil {
				t.Fatal(err)
			}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			credentialID := bytes.Repeat([]byte{0x5c}, 32)
			attestation := signedRegistrationAttestation(t, begin, key, credentialID, "https://money.example.test", true, nil)
			assertBegin := beginRegistrationAssertion(t, service, begin, attestation)
			origin := "https://money.example.test"
			if tc == "wrong-origin" {
				origin = "https://attacker.example"
			}
			candidate := control.PasskeyCredential{ID: credentialID}
			signingKey := key
			if tc == "foreign-credential" {
				candidate.ID = bytes.Repeat([]byte{0x99}, 32)
			}
			if tc == "wrong-signature" {
				signingKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
			}
			assertionBegin := assertBegin
			if tc == "wrong-challenge" {
				altered := *assertBegin.Options
				altered.Response.Challenge = protocol.URLEncodedBase64(bytes.Repeat([]byte{0x7a}, 32))
				assertionBegin = PasskeyLoginBegin{CeremonyID: assertBegin.CeremonyID, Options: &altered}
			}
			assertion := signedPrivacyAssertion(t, assertionBegin, candidate, signingKey, origin, tc != "no-uv")
			input := FinishPasskeyRegistrationAssertionInput{
				CeremonyID: assertBegin.CeremonyID, ClientKey: "client", Name: "Invalid",
				Password: []byte(registrationTestPassword), CredentialJSON: assertion,
				PRFResult: bytes.Repeat([]byte{0x2c}, keyenvelope.PasskeySecretSize),
			}
			if tc == "wrong-client" {
				input.ClientKey = "other"
			}
			if tc == "expired" {
				ceremony := service.ceremonies[assertBegin.CeremonyID]
				ceremony.Session.Expires = time.Now().Add(-time.Second)
				service.ceremonies[assertBegin.CeremonyID] = ceremony
			}
			if _, err := service.FinishPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, input, serverAuthTestNow); err == nil {
				t.Fatal("invalid assertion completed registration")
			}
			if len(store.created) != 0 {
				t.Fatal("invalid assertion persisted a credential")
			}
		})
	}
}

func TestPasskeyRegistrationAssertionCeremonyIsSingleUse(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x5e}, 32)
	attestation := signedRegistrationAttestation(t, begin, key, credentialID, "https://money.example.test", true, nil)
	assertBegin := beginRegistrationAssertion(t, service, begin, attestation)
	if _, err := service.BeginPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, BeginPasskeyRegistrationAssertionInput{
		CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: attestation,
	}); !errors.Is(err, ErrPasskeyCeremony) {
		t.Fatalf("registration ceremony replay error = %v", err)
	}
	assertion := signedPrivacyAssertion(t, assertBegin, control.PasskeyCredential{ID: credentialID}, key, "https://money.example.test", true)
	input := FinishPasskeyRegistrationAssertionInput{
		CeremonyID: assertBegin.CeremonyID, ClientKey: "client", Name: "Once",
		Password: []byte(registrationTestPassword), CredentialJSON: assertion,
		PRFResult: bytes.Repeat([]byte{0x2f}, keyenvelope.PasskeySecretSize),
	}
	if _, err := service.FinishPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, input, serverAuthTestNow); err != nil {
		t.Fatalf("valid registration assertion failed: %v", err)
	}
	if _, err := service.FinishPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, input, serverAuthTestNow); !errors.Is(err, ErrPasskeyCeremony) {
		t.Fatalf("replayed ceremony error = %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("credential writes = %d", len(store.created))
	}
}

func TestPasskeyRegistrationAssertionRequiresThirtyTwoBytePRF(t *testing.T) {
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	service, store := newRegistrationTestService(t, dek)
	ctx := context.Background()
	begin, err := service.BeginPasskeyRegistration(ctx, serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := bytes.Repeat([]byte{0x5d}, 32)
	attestation := signedRegistrationAttestation(t, begin, key, credentialID, "https://money.example.test", true, nil)
	assertBegin := beginRegistrationAssertion(t, service, begin, attestation)
	assertion := signedPrivacyAssertion(t, assertBegin, control.PasskeyCredential{ID: credentialID}, key, "https://money.example.test", true)
	if _, err := service.FinishPasskeyRegistrationAssertion(ctx, serverAuthTestUserID, FinishPasskeyRegistrationAssertionInput{
		CeremonyID: assertBegin.CeremonyID, ClientKey: "client", Name: "Short PRF",
		Password: []byte(registrationTestPassword), CredentialJSON: assertion,
		PRFResult: bytes.Repeat([]byte{0x2d}, keyenvelope.PasskeySecretSize-1),
	}, serverAuthTestNow); !errors.Is(err, ErrPasskeyPRFRequired) {
		t.Fatalf("short PRF result error = %v", err)
	}
	if len(store.created) != 0 {
		t.Fatal("short PRF result persisted a credential")
	}
}
