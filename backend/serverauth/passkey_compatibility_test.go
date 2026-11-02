package serverauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
)

// Frozen credentials exercise the storage/wire contract independently of the
// CBOR encoder under test. Keep them unchanged across dependency upgrades.
type passkeyCredentialFixture struct {
	Name                     string `json:"name"`
	PrivateKey               string `json:"test_private_key_pkcs8_b64"`
	CredentialID             string `json:"credential_id_b64"`
	PublicKey                string `json:"public_key_cbor_b64"`
	Attestation              string `json:"attestation_object_b64"`
	AttestationWithExtension string `json:"attestation_with_extension_b64"`
}

func passkeyCredentialFixtures(t *testing.T) []passkeyCredentialFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/passkey-credentials-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Version     int                        `json:"schema_version"`
		RPID        string                     `json:"rp_id"`
		Origin      string                     `json:"origin"`
		Credentials []passkeyCredentialFixture `json:"credentials"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != 1 || corpus.RPID != "money.example.test" || corpus.Origin != "https://money.example.test" || len(corpus.Credentials) != 3 {
		t.Fatal("unexpected passkey fixture schema")
	}
	return corpus.Credentials
}

func fixtureBytes(t *testing.T, encoded string) []byte {
	t.Helper()
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func fixtureRegistrationResponse(t *testing.T, begin PasskeyRegistrationBegin, f passkeyCredentialFixture, object []byte) json.RawMessage {
	t.Helper()
	client, err := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": begin.Options.Response.Challenge.String(), "origin": "https://money.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	response, err := json.Marshal(map[string]any{
		"id": encode(fixtureBytes(t, f.CredentialID)), "rawId": encode(fixtureBytes(t, f.CredentialID)), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": encode(client), "attestationObject": encode(object), "transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func fixtureAssertion(t *testing.T, begin PasskeyLoginBegin, f passkeyCredentialFixture, count uint32) json.RawMessage {
	t.Helper()
	return fixtureAssertionFor(t, begin, f, count, "https://money.example.test", 0x05)
}

func fixtureAssertionFor(t *testing.T, begin PasskeyLoginBegin, f passkeyCredentialFixture, count uint32, origin string, flags byte) json.RawMessage {
	t.Helper()
	key, err := x509.ParsePKCS8PrivateKey(fixtureBytes(t, f.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		t.Fatal("fixture key is not a signer")
	}
	client, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": begin.Options.Response.Challenge.String(), "origin": origin})
	if err != nil {
		t.Fatal(err)
	}
	rp := sha256.Sum256([]byte("money.example.test"))
	auth := append(append([]byte{}, rp[:]...), flags)
	auth = binary.BigEndian.AppendUint32(auth, count)
	clientHash := sha256.Sum256(client)
	message := append(append([]byte{}, auth...), clientHash[:]...)
	var signature []byte
	if _, ok := key.(ed25519.PrivateKey); ok {
		signature, err = signer.Sign(rand.Reader, message, crypto.Hash(0))
	} else {
		digest := sha256.Sum256(message)
		signature, err = signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	}
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	id := encode(fixtureBytes(t, f.CredentialID))
	response, err := json.Marshal(map[string]any{"id": id, "rawId": id, "type": "public-key", "response": map[string]any{
		"clientDataJSON": encode(client), "authenticatorData": encode(auth), "signature": encode(signature), "userHandle": encode([]byte(serverAuthTestUserID)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func seedStoredCredential(t *testing.T, f passkeyCredentialFixture, prf bool) (*Service, *registrationTestStore) {
	t.Helper()
	dek := bytes.Repeat([]byte{37}, keyenvelope.DEKSize)
	s, store := newRegistrationTestService(t, dek)
	id := fixtureBytes(t, f.CredentialID)
	binding := keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID}
	envelope, err := keyenvelope.WrapWithServerPasskey(dek, s.passkeyCustodyKey[:], binding, id)
	if prf {
		envelope, err = keyenvelope.WrapWithPasskey(dek, bytes.Repeat([]byte{41}, 32), binding)
	}
	if err != nil {
		t.Fatal(err)
	}
	store.records = []control.PasskeyCredential{{ID: id, UserID: serverAuthTestUserID,
		Credential: webauthn.Credential{ID: id, PublicKey: fixtureBytes(t, f.PublicKey)},
		PRFSalt:    bytes.Clone(discoverablePRFSalt[:]), VaultEnvelope: *envelope}}
	return s, store
}

func TestPasskeyRegressionStoredCredentials(t *testing.T) {
	for _, f := range passkeyCredentialFixtures(t) {
		for _, prf := range []bool{false, true} {
			t.Run(f.Name+map[bool]string{false: "/server-custody", true: "/legacy-prf"}[prf], func(t *testing.T) {
				s, store := seedStoredCredential(t, f, prf)
				begin, err := s.BeginDiscoverablePasskeyLogin(context.Background(), "client")
				if err != nil {
					t.Fatal(err)
				}
				input := FinishPasskeyLoginInput{CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: fixtureAssertion(t, begin, f, 1)}
				if prf {
					input.PRFResult = bytes.Repeat([]byte{41}, 32)
				}
				session, err := s.FinishDiscoverablePasskeyLogin(context.Background(), input, time.Now().UTC())
				if err != nil || session == nil || session.UserID != serverAuthTestUserID {
					t.Fatalf("old public key login: session=%v err=%v", session, err)
				}
				if store.records[0].Credential.Authenticator.SignCount != 1 {
					t.Fatal("login did not persist signature counter")
				}
				finishFixtureReauthentication(t, s, store, f)
			})
		}
	}
}

func finishFixtureReauthentication(t *testing.T, s *Service, store *registrationTestStore, f passkeyCredentialFixture) {
	t.Helper()
	begin, err := s.BeginPasskeyReauthentication(context.Background(), serverAuthTestUserID, "client")
	if err != nil {
		t.Fatal(err)
	}
	input := FinishPasskeyLoginInput{CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: fixtureAssertion(t, begin, f, 2)}
	if err := s.FinishPasskeyReauthentication(context.Background(), serverAuthTestUserID, input, time.Now().UTC()); err != nil {
		t.Fatalf("old public key reauthentication: %v", err)
	}
	if store.records[0].Credential.Authenticator.SignCount != 2 {
		t.Fatal("reauthentication did not persist signature counter")
	}
	if err := s.FinishPasskeyReauthentication(context.Background(), serverAuthTestUserID, input, time.Now().UTC()); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reauthentication replay: %v", err)
	}
}

func TestPasskeyRegressionRegistration(t *testing.T) {
	for _, f := range passkeyCredentialFixtures(t) {
		for _, ext := range []bool{false, true} {
			t.Run(f.Name+map[bool]string{false: "/plain", true: "/credProtect"}[ext], func(t *testing.T) {
				s, store := newRegistrationTestService(t, bytes.Repeat([]byte{37}, 32))
				begin, err := s.BeginPasskeyRegistration(context.Background(), serverAuthTestUserID, "client")
				if err != nil {
					t.Fatal(err)
				}
				object := f.Attestation
				if ext {
					object = f.AttestationWithExtension
				}
				_, err = s.FinishPasskeyRegistration(context.Background(), serverAuthTestUserID, FinishPasskeyRegistrationInput{
					CeremonyID: begin.CeremonyID, ClientKey: "client", Name: f.Name, CredentialJSON: fixtureRegistrationResponse(t, begin, f, fixtureBytes(t, object)),
				}, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if len(store.records) != 1 || !bytes.Equal(store.records[0].Credential.PublicKey, fixtureBytes(t, f.PublicKey)) {
					t.Fatal("old registration public key was not preserved")
				}
				finishFixtureReauthentication(t, s, store, f)
			})
		}
	}
}

func TestPasskeyRegressionReauthenticationRejectsInvalidAssertions(t *testing.T) {
	f := passkeyCredentialFixtures(t)[0]
	for _, bad := range []string{"signature", "challenge", "origin", "no-uv", "other-user", "expired", "wrong-client", "login-ceremony"} {
		t.Run(bad, func(t *testing.T) {
			s, store := seedStoredCredential(t, f, false)
			begin, err := s.BeginPasskeyReauthentication(context.Background(), serverAuthTestUserID, "client")
			if bad == "login-ceremony" {
				begin, err = s.BeginPasskeyLogin(context.Background(), "person@example.test", "client")
			}
			if err != nil {
				t.Fatal(err)
			}
			signingBegin := begin
			origin, flags := "https://money.example.test", byte(0x05)
			if bad == "origin" {
				origin = "https://attacker.example"
			}
			if bad == "no-uv" {
				flags &^= 4
			}
			if bad == "challenge" {
				options := *begin.Options
				options.Response.Challenge = []byte("wrong-challenge")
				signingBegin.Options = &options
			}
			payload := fixtureAssertionFor(t, signingBegin, f, 1, origin, flags)
			var response map[string]any
			if err := json.Unmarshal(payload, &response); err != nil {
				t.Fatal(err)
			}
			if bad == "signature" {
				response["response"].(map[string]any)["signature"] = base64.RawURLEncoding.EncodeToString([]byte("invalid-signature"))
			}
			payload, err = json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			user, client := serverAuthTestUserID, "client"
			if bad == "other-user" {
				user = "other-user"
			}
			if bad == "wrong-client" {
				client = "other-client"
			}
			if bad == "expired" {
				ceremony := s.ceremonies[begin.CeremonyID]
				ceremony.Session.Expires = time.Now().Add(-time.Minute)
				s.ceremonies[begin.CeremonyID] = ceremony
			}
			err = s.FinishPasskeyReauthentication(context.Background(), user, FinishPasskeyLoginInput{CeremonyID: begin.CeremonyID, ClientKey: client, CredentialJSON: payload}, time.Now().UTC())
			if !errors.Is(err, ErrInvalidCredentials) || store.records[0].Credential.Authenticator.SignCount != 0 {
				t.Fatalf("invalid reauthentication accepted or committed: %v", err)
			}
		})
	}
}

// Build the outer map manually so negative inputs are independent of the
// decoder/encoder under test. authData is a byte string, not nested CBOR here.
func rawAttestation(auth []byte) []byte {
	out := []byte{0xa3, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e', 0x67, 'a', 't', 't', 'S', 't', 'm', 't', 0xa0, 0x68, 'a', 'u', 't', 'h', 'D', 'a', 't', 'a'}
	out = append(out, 0x59)
	out = binary.BigEndian.AppendUint16(out, uint16(len(auth)))
	return append(out, auth...)
}

func TestPasskeyRegressionMalformedRegistrationIsRejected(t *testing.T) {
	f := passkeyCredentialFixtures(t)[0]
	key, id := fixtureBytes(t, f.PublicKey), fixtureBytes(t, f.CredentialID)
	rp := sha256.Sum256([]byte("money.example.test"))
	prefix := append(append([]byte{}, rp[:]...), 0x45, 0, 0, 0, 0)
	prefix = append(prefix, make([]byte, 16)...)
	prefix = binary.BigEndian.AppendUint16(prefix, uint16(len(id)))
	prefix = append(prefix, id...)
	t.Run("valid-manual-wrapper", func(t *testing.T) {
		s, store := newRegistrationTestService(t, bytes.Repeat([]byte{37}, 32))
		begin, err := s.BeginPasskeyRegistration(context.Background(), serverAuthTestUserID, "client")
		if err != nil {
			t.Fatal(err)
		}
		object := rawAttestation(append(bytes.Clone(prefix), key...))
		_, err = s.FinishPasskeyRegistration(context.Background(), serverAuthTestUserID, FinishPasskeyRegistrationInput{
			CeremonyID: begin.CeremonyID, ClientKey: "client", Name: "valid-control", CredentialJSON: fixtureRegistrationResponse(t, begin, f, object),
		}, time.Now().UTC())
		if err != nil || len(store.records) != 1 {
			t.Fatalf("manual outer encoding must work before testing malformed inner CBOR: %v", err)
		}
	})
	object := fixtureBytes(t, f.Attestation)
	duplicateOuter := append([]byte{0xa4}, object[1:]...)
	duplicateOuter = append(duplicateOuter, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e')
	indefiniteOuter := append(append([]byte{0xbf}, object[1:]...), 0xff)
	duplicateKey := append([]byte{key[0] + 1}, key[1:]...)
	duplicateKey = append(duplicateKey, 3, 0x26)
	deepKey := append([]byte{key[0] + 1}, key[1:]...)
	deepKey = append(deepKey, 0x18, 99, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0)
	cases := map[string][]byte{
		"outer-truncated": object[:len(object)-1], "outer-trailing": append(bytes.Clone(object), 0),
		"outer-tag": append([]byte{0xd9, 0xd9, 0xf7}, object...), "outer-indefinite": indefiniteOuter, "outer-duplicate": duplicateOuter,
		"outer-invalid-utf8": bytes.Replace(object, []byte{0x63, 'f', 'm', 't'}, []byte{0x63, 0xff, 'm', 't'}, 1),
	}
	for name, badKey := range map[string][]byte{
		"truncated": key[:len(key)-1], "trailing": append(bytes.Clone(key), 0), "duplicate": duplicateKey,
		"tag": append([]byte{0xd9, 0xd9, 0xf7}, key...), "indefinite": append(append([]byte{0xbf}, key[1:]...), 0xff), "depth": deepKey,
	} {
		cases["key-"+name] = rawAttestation(append(bytes.Clone(prefix), badKey...))
	}
	for name, extension := range map[string][]byte{
		"truncated": {0xa1, 0x61, 'x'}, "trailing": {0xa0, 0}, "duplicate": {0xa2, 0x61, 'x', 1, 0x61, 'x', 2},
		"tag": {0xd9, 0xd9, 0xf7, 0xa0}, "indefinite": {0xbf, 0xff}, "depth": {0xa1, 0x61, 'x', 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0},
	} {
		auth := append(bytes.Clone(prefix), key...)
		auth[32] |= 0x80
		cases["extension-"+name] = rawAttestation(append(auth, extension...))
	}
	for name, object := range cases {
		t.Run(name, func(t *testing.T) {
			s, store := newRegistrationTestService(t, bytes.Repeat([]byte{37}, 32))
			begin, err := s.BeginPasskeyRegistration(context.Background(), serverAuthTestUserID, "client")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.FinishPasskeyRegistration(context.Background(), serverAuthTestUserID, FinishPasskeyRegistrationInput{CeremonyID: begin.CeremonyID, ClientKey: "client", Name: "invalid", CredentialJSON: fixtureRegistrationResponse(t, begin, f, object)}, time.Now().UTC())
			if err == nil || len(store.records) != 0 || len(store.created) != 0 {
				t.Fatalf("invalid CBOR registered a credential: %v", err)
			}
		})
	}
}
