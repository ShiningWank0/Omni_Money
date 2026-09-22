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
	"reflect"
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

type privacyTestStore struct {
	*fakeControlStore
	PasskeyControlStore
	user     control.UserSummary
	records  []control.PasskeyCredential
	recorded int
	queries  int
}

func (s *privacyTestStore) ListPasskeyCredentials(_ context.Context, id string) ([]control.PasskeyCredential, error) {
	s.queries++
	if id == s.user.ID {
		return s.records, nil
	}
	return nil, nil
}

func (s *privacyTestStore) RecordSuccessfulPasskeyUse(_ context.Context, expected control.PasskeyCredential, updated webauthn.Credential, _ time.Time, login bool) error {
	if !login || !bytes.Equal(expected.ID, updated.ID) {
		return errors.New("unexpected credential commit")
	}
	s.recorded++
	return nil
}

func privacyTestService(t *testing.T, store *privacyTestStore) *Service {
	t.Helper()
	store.fakeControlStore = &fakeControlStore{
		getUserByEmailFn: func(_ context.Context, email string) (control.UserSummary, error) {
			if email == store.user.Email {
				return store.user, nil
			}
			return control.UserSummary{}, control.ErrNotFound
		},
		getUserFn: func(_ context.Context, id string) (control.UserSummary, error) {
			if id == store.user.ID {
				return store.user, nil
			}
			t.Fatal("dummy or foreign identity reached authenticated lookup")
			return control.UserSummary{}, control.ErrNotFound
		},
		lookupVaultIDFn: func(context.Context, string) (string, error) { return serverAuthTestVaultID, nil },
	}
	verifier, err := webauthn.New(&webauthn.Config{
		RPID: "money.example.test", RPDisplayName: "Omni Money", RPOrigins: []string{"https://money.example.test"},
		Timeouts: webauthn.TimeoutsConfig{Login: webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		Store: store, WebAuthn: verifier, PasskeyPrivacyKey: bytes.Repeat([]byte{71}, 32),
		Sessions: &fakeSessionInvalidator{}, Vaults: &fakeVaultDrainer{},
		OpenSession: func(user control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
			defer key.Destroy()
			if user.ID != store.user.ID || vaultID != serverAuthTestVaultID || !bytes.Equal(key[:], bytes.Repeat([]byte{37}, 32)) {
				t.Fatal("login opened the wrong vault or used the wrong key")
			}
			return &middleware.Session{UserID: user.ID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func privacyTestRecord(t *testing.T, seed byte) (control.PasskeyCredential, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keyenvelope.WrapWithPasskey(bytes.Repeat([]byte{37}, 32), bytes.Repeat([]byte{41}, 32), keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID})
	if err != nil {
		t.Fatal(err)
	}
	lengths := [...]int{16, 32, 64, 128}
	id := bytes.Repeat([]byte{seed}, lengths[int(seed)%len(lengths)])
	return control.PasskeyCredential{
		ID: id, UserID: serverAuthTestUserID, PRFSalt: bytes.Repeat([]byte{seed}, 32), VaultEnvelope: *envelope,
		Credential: webauthn.Credential{ID: id, PublicKey: public, Transport: []protocol.AuthenticatorTransport{protocol.Internal}},
	}, key
}

func TestPasskeyLoginBeginConcealsMissingDisabledAndPasswordOnlyAccounts(t *testing.T) {
	for _, tc := range []struct {
		name              string
		count             int
		disabled, missing bool
	}{
		{name: "missing", missing: true}, {name: "password-only"}, {name: "disabled", count: 1, disabled: true},
		{name: "one", count: 1}, {name: "multiple", count: 3}, {name: "maximum", count: control.MaxPasskeysPerUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &privacyTestStore{user: control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", State: control.UserActive}}
			if tc.disabled {
				store.user.State = control.UserDisabled
			}
			for i := 0; i < tc.count; i++ {
				record, _ := privacyTestRecord(t, byte(i+1))
				store.records = append(store.records, record)
			}
			service := privacyTestService(t, store)
			email := store.user.Email
			if tc.missing {
				email = "missing@example.test"
			}
			first, err := service.BeginPasskeyLogin(context.Background(), email, "client")
			if err != nil {
				t.Fatal(err)
			}
			second, err := service.BeginPasskeyLogin(context.Background(), email, "client")
			if err != nil {
				t.Fatal(err)
			}
			if store.queries != 2 {
				t.Fatalf("credential queries = %d", store.queries)
			}
			options := first.Options.Response
			if len(options.AllowedCredentials) != control.MaxPasskeysPerUser || options.UserVerification != protocol.VerificationRequired {
				t.Fatal("public request shape differs")
			}
			for _, descriptor := range options.AllowedCredentials {
				if len(descriptor.Transport) != 0 {
					t.Fatal("transport metadata leaked")
				}
			}
			if first.CeremonyID == second.CeremonyID || bytes.Equal(options.Challenge, second.Options.Response.Challenge) {
				t.Fatal("challenge or ceremony reused")
			}
			if !reflect.DeepEqual(options.AllowedCredentials, second.Options.Response.AllowedCredentials) || !reflect.DeepEqual(options.Extensions, second.Options.Response.Extensions) {
				t.Fatal("decoys are distinguishable across requests")
			}
			// Service restarts must not rotate only the dummy metadata.
			restarted := privacyTestService(t, store)
			third, err := restarted.BeginPasskeyLogin(context.Background(), email, "client")
			if err != nil || !reflect.DeepEqual(options.AllowedCredentials, third.Options.Response.AllowedCredentials) || !reflect.DeepEqual(options.Extensions, third.Options.Response.Extensions) {
				t.Fatal("restart changed decoy metadata")
			}
			ceremony := service.ceremonies[first.CeremonyID]
			if tc.missing || tc.disabled || tc.count == 0 {
				if ceremony.UserID != "" || len(ceremony.Session.AllowedCredentialIDs) != 0 {
					t.Fatal("dummy ceremony retained a login identity")
				}
				_, err = service.FinishPasskeyLogin(context.Background(), FinishPasskeyLoginInput{CeremonyID: first.CeremonyID, ClientKey: "client", PRFResult: bytes.Repeat([]byte{41}, 32)}, time.Now())
				if !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("dummy finish = %v", err)
				}
			} else if len(ceremony.Session.AllowedCredentialIDs) != tc.count {
				t.Fatal("padding entered the server verification allowlist")
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(serverAuthTestUserID)) || bytes.Contains(encoded, []byte(email)) {
				t.Fatal("identity leaked in public response")
			}
		})
	}
}

func signedPrivacyAssertion(t *testing.T, begin PasskeyLoginBegin, record control.PasskeyCredential, key *ecdsa.PrivateKey, origin string, uv bool) []byte {
	t.Helper()
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": begin.Options.Response.Challenge.String(), "origin": origin})
	rp := sha256.Sum256([]byte("money.example.test"))
	auth := make([]byte, 37)
	copy(auth, rp[:])
	auth[32] = 1
	if uv {
		auth[32] |= 4
	}
	binary.BigEndian.PutUint32(auth[33:], 1)
	clientHash := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte(nil), auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	result, err := json.Marshal(map[string]any{"id": encode(record.ID), "rawId": encode(record.ID), "type": "public-key", "response": map[string]any{
		"clientDataJSON": encode(client), "authenticatorData": encode(auth), "signature": encode(signature), "userHandle": encode([]byte(serverAuthTestUserID)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPaddedPasskeyLoginStillRequiresValidAssertionAndVaultSecret(t *testing.T) {
	record, key := privacyTestRecord(t, 1)
	store := &privacyTestStore{user: control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", State: control.UserActive}, records: []control.PasskeyCredential{record}}
	service := privacyTestService(t, store)
	for _, tc := range []string{"valid", "wrong-origin", "no-uv", "wrong-prf", "wrong-signature", "foreign-credential", "wrong-client", "expired", "dummy-ceremony"} {
		t.Run(tc, func(t *testing.T) {
			email := store.user.Email
			if tc == "dummy-ceremony" {
				email = "missing@example.test"
			}
			begin, err := service.BeginPasskeyLogin(context.Background(), email, "client")
			if err != nil {
				t.Fatal(err)
			}
			origin := "https://money.example.test"
			if tc == "wrong-origin" {
				origin = "https://attacker.example"
			}
			candidate := record
			if tc == "foreign-credential" {
				candidate.ID = bytes.Repeat([]byte{99}, 64)
			}
			signingKey := key
			if tc == "wrong-signature" {
				_, signingKey = privacyTestRecord(t, 99)
			}
			input := FinishPasskeyLoginInput{CeremonyID: begin.CeremonyID, ClientKey: "client", PRFResult: bytes.Repeat([]byte{41}, 32), CredentialJSON: signedPrivacyAssertion(t, begin, candidate, signingKey, origin, tc != "no-uv")}
			if tc == "wrong-prf" {
				input.PRFResult[0] ^= 1
			}
			if tc == "wrong-client" {
				input.ClientKey = "other"
			}
			if tc == "expired" {
				c := service.ceremonies[begin.CeremonyID]
				c.Session.Expires = time.Now().Add(-time.Second)
				service.ceremonies[begin.CeremonyID] = c
			}
			session, err := service.FinishPasskeyLogin(context.Background(), input, time.Now())
			if tc == "valid" {
				if err != nil || session == nil || session.UserID != store.user.ID {
					t.Fatalf("real login failed: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidCredentials) || session != nil {
				t.Fatalf("invalid login accepted: %v", err)
			}
			if _, err := service.FinishPasskeyLogin(context.Background(), input, time.Now()); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal("ceremony replay was accepted")
			}
		})
	}
	if store.recorded != 1 {
		t.Fatalf("successful credential writes = %d", store.recorded)
	}
}

func TestPasskeyPrivacyRequiresPersistentSecretAndSeparatesEmails(t *testing.T) {
	store := &privacyTestStore{user: control.UserSummary{ID: serverAuthTestUserID, Email: "person@example.test", State: control.UserActive}}
	service := privacyTestService(t, store)
	for _, key := range [][]byte{nil, make([]byte, 32), bytes.Repeat([]byte{1}, 31)} {
		if _, err := NewService(Dependencies{Store: store, WebAuthn: service.webauthn, PasskeyPrivacyKey: key,
			OpenSession: service.openSession, Sessions: service.sessions, Vaults: service.vaults}); err == nil {
			t.Fatal("passkey service accepted a missing or invalid privacy key")
		}
	}
	first, err := service.BeginPasskeyLogin(context.Background(), " Missing@Example.Test ", "client")
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := service.BeginPasskeyLogin(context.Background(), "missing@example.test", "client")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.BeginPasskeyLogin(context.Background(), "other@example.test", "client")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Options.Response.AllowedCredentials, normalized.Options.Response.AllowedCredentials) ||
		reflect.DeepEqual(first.Options.Response.AllowedCredentials, other.Options.Response.AllowedCredentials) {
		t.Fatal("decoys must be stable for normalized emails and distinct between emails")
	}
	lengths := map[int]bool{}
	for _, descriptor := range first.Options.Response.AllowedCredentials {
		length := len(descriptor.CredentialID)
		if length < 16 || length > 1023 {
			t.Fatal("decoy ID is outside the WebAuthn size range")
		}
		lengths[length] = true
	}
	if len(lengths) < 2 {
		t.Fatal("decoys reveal a single fixed credential ID length")
	}
}
