package keyenvelope

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var testContext = Context{UserID: "user-123", VaultID: "vault-456"}

func TestPasswordEnvelopeRoundTripAndVerifier(t *testing.T) {
	dek := testBytes(DEKSize, 0x31)
	password := []byte("correct horse battery staple")
	envelope, err := WrapWithPassword(dek, password, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Profile != DefaultProfile() {
		t.Fatalf("unexpected Argon2id profile: %+v", envelope.Profile)
	}
	if envelope.Kind != KindPassword || envelope.Version != CurrentVersion || envelope.KDF != passwordKDF {
		t.Fatalf("unexpected envelope metadata: %+v", envelope)
	}
	if bytes.Contains(envelope.Ciphertext, dek) {
		t.Fatal("ciphertext contains plaintext DEK")
	}

	verified, err := VerifyPassword(envelope, password, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("correct password did not verify")
	}
	verified, err = VerifyPassword(envelope, []byte("wrong password"), testContext)
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		t.Fatal("wrong password verified")
	}

	got, err := UnwrapWithPassword(envelope, password, testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped DEK differs")
	}

	got[0] ^= 0xff
	if got[0] == dek[0] {
		t.Fatal("returned DEK unexpectedly aliases input")
	}
}

func TestPasswordEnvelopeSurvivesJSONRoundTrip(t *testing.T) {
	dek := testBytes(DEKSize, 0x44)
	envelope, err := WrapWithPassword(dek, []byte("json password"), testContext)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(dek)) {
		t.Fatal("JSON unexpectedly contains plaintext key material")
	}
	var decoded Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapWithPassword(&decoded, []byte("json password"), testContext)
	if err != nil {
		t.Fatal(err)
	}
	clear(got)
}

func TestPasswordEnvelopeRejectsWrongPasswordAndAAD(t *testing.T) {
	envelope, err := WrapWithPassword(testBytes(DEKSize, 0x52), []byte("right password"), testContext)
	if err != nil {
		t.Fatal(err)
	}

	for name, attempt := range map[string]func() error{
		"wrong password": func() error {
			_, err := UnwrapWithPassword(envelope, []byte("wrong password"), testContext)
			return err
		},
		"wrong user": func() error {
			_, err := UnwrapWithPassword(envelope, []byte("right password"), Context{UserID: "another-user", VaultID: testContext.VaultID})
			return err
		},
		"wrong vault": func() error {
			_, err := UnwrapWithPassword(envelope, []byte("right password"), Context{UserID: testContext.UserID, VaultID: "another-vault"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("got %v, want ErrAuthentication", err)
			}
		})
	}
}

func TestPasswordEnvelopeRejectsTampering(t *testing.T) {
	original, err := WrapWithPassword(testBytes(DEKSize, 0x63), []byte("tamper password"), testContext)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Envelope){
		"salt":       func(envelope *Envelope) { envelope.Salt[0] ^= 1 },
		"nonce":      func(envelope *Envelope) { envelope.Nonce[0] ^= 1 },
		"ciphertext": func(envelope *Envelope) { envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 1 },
		"verifier":   func(envelope *Envelope) { envelope.Verifier[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := cloneEnvelope(original)
			mutate(tampered)
			dek, err := UnwrapWithPassword(tampered, []byte("tamper password"), testContext)
			clear(dek)
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("got %v, want ErrAuthentication", err)
			}
		})
	}
}

func TestPasswordEnvelopeRejectsUntrustedProfilesBeforeDerivation(t *testing.T) {
	original, err := WrapWithPassword(testBytes(DEKSize, 0x74), []byte("profile password"), testContext)
	if err != nil {
		t.Fatal(err)
	}

	profiles := map[string]Argon2idProfile{
		"zero":        {},
		"downgraded":  {MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1},
		"huge memory": {MemoryKiB: ^uint32(0), Iterations: 3, Parallelism: 2},
		"huge passes": {MemoryKiB: 64 * 1024, Iterations: ^uint32(0), Parallelism: 2},
		"parallelism": {MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 255},
	}
	for name, profile := range profiles {
		t.Run(name, func(t *testing.T) {
			tampered := cloneEnvelope(original)
			tampered.Profile = profile
			if _, err := UnwrapWithPassword(tampered, []byte("profile password"), testContext); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("got %v, want ErrInvalidEnvelope", err)
			}
		})
	}
}

func TestPasswordEnvelopeBindsKindAndVersion(t *testing.T) {
	original, err := WrapWithPassword(testBytes(DEKSize, 0x19), []byte("metadata password"), testContext)
	if err != nil {
		t.Fatal(err)
	}

	tamperedKind := cloneEnvelope(original)
	tamperedKind.Kind = KindRecovery
	if _, err := UnwrapWithPassword(tamperedKind, []byte("metadata password"), testContext); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("kind: got %v, want ErrInvalidEnvelope", err)
	}
	tamperedVersion := cloneEnvelope(original)
	tamperedVersion.Version++
	if _, err := UnwrapWithPassword(tamperedVersion, []byte("metadata password"), testContext); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("version: got %v, want ErrInvalidEnvelope", err)
	}

	aad := authenticatedData(testContext, KindPassword, CurrentVersion)
	if bytes.Equal(aad, authenticatedData(testContext, KindRecovery, CurrentVersion)) {
		t.Fatal("kind is not bound into AAD")
	}
	if bytes.Equal(aad, authenticatedData(testContext, KindPassword, CurrentVersion+1)) {
		t.Fatal("version is not bound into AAD")
	}
}

func TestRecoveryEnvelopeRoundTripAndIsolation(t *testing.T) {
	dek := testBytes(DEKSize, 0x27)
	secret, err := GenerateRecoverySecret()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(secret)
	envelope, err := WrapWithRecovery(dek, secret, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Profile != (Argon2idProfile{}) || len(envelope.Verifier) != 0 {
		t.Fatal("recovery envelope contains password metadata")
	}
	got, err := UnwrapWithRecovery(envelope, secret, testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, dek) {
		t.Fatal("recovery envelope returned the wrong DEK")
	}

	wrongSecret := append([]byte(nil), secret...)
	wrongSecret[0] ^= 1
	defer clear(wrongSecret)
	if _, err := UnwrapWithRecovery(envelope, wrongSecret, testContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong recovery secret: got %v, want ErrAuthentication", err)
	}
	if _, err := UnwrapWithRecovery(envelope, secret, Context{UserID: "other", VaultID: testContext.VaultID}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong recovery AAD: got %v, want ErrAuthentication", err)
	}

	tampered := cloneEnvelope(envelope)
	tampered.Ciphertext[0] ^= 1
	if _, err := UnwrapWithRecovery(tampered, secret, testContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("tampering: got %v, want ErrAuthentication", err)
	}
}

func TestRecoveryEnvelopeRequiresExactly256BitSecret(t *testing.T) {
	dek := testBytes(DEKSize, 0x38)
	for _, size := range []int{0, RecoverySecretSize - 1, RecoverySecretSize + 1} {
		if _, err := WrapWithRecovery(dek, make([]byte, size), testContext); !errors.Is(err, ErrInvalidRecoverySecret) {
			t.Fatalf("size %d: got %v, want ErrInvalidRecoverySecret", size, err)
		}
	}
}

func TestPasskeyEnvelopeRoundTripAndIsolation(t *testing.T) {
	dek := testBytes(DEKSize, 0x61)
	secret := testBytes(PasskeySecretSize, 0x72)
	envelope, err := WrapWithPasskey(dek, secret, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != KindPasskey || envelope.KDF != passkeyKDF {
		t.Fatalf("unexpected passkey metadata: %+v", envelope)
	}
	got, err := UnwrapWithPasskey(envelope, secret, testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if !bytes.Equal(got, dek) {
		t.Fatal("passkey envelope returned the wrong DEK")
	}
	wrong := append([]byte(nil), secret...)
	wrong[0] ^= 1
	if _, err := UnwrapWithPasskey(envelope, wrong, testContext); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong passkey secret: got %v", err)
	}
	if _, err := UnwrapWithRecovery(envelope, secret, testContext); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("passkey envelope was accepted as recovery envelope: %v", err)
	}
}

func TestPurposeSeparatedHKDFKeys(t *testing.T) {
	root := testBytes(DEKSize, 0xa5)
	salt := testBytes(SaltSize, 0x5a)
	passwordAEAD, err := deriveKey(root, salt, hkdfInfo(KindPassword, "aead"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(passwordAEAD)
	passwordVerifierKey, err := deriveKey(root, salt, hkdfInfo(KindPassword, "verifier"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(passwordVerifierKey)
	recoveryAEAD, err := deriveKey(root, salt, hkdfInfo(KindRecovery, "aead"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(recoveryAEAD)
	if bytes.Equal(passwordAEAD, passwordVerifierKey) || bytes.Equal(passwordAEAD, recoveryAEAD) || bytes.Equal(passwordVerifierKey, recoveryAEAD) {
		t.Fatal("HKDF purpose separation produced identical keys")
	}
}

func TestInputValidation(t *testing.T) {
	if _, err := WrapWithPassword(make([]byte, DEKSize-1), []byte("password"), testContext); !errors.Is(err, ErrInvalidDEK) {
		t.Fatalf("DEK: got %v", err)
	}
	if _, err := WrapWithPassword(make([]byte, DEKSize), nil, testContext); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("password: got %v", err)
	}
	if _, err := WrapWithPassword(make([]byte, DEKSize), []byte("password"), Context{}); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("context: got %v", err)
	}
	if _, err := UnwrapWithPassword(nil, []byte("password"), testContext); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("nil envelope: got %v", err)
	}
}

func cloneEnvelope(envelope *Envelope) *Envelope {
	clone := *envelope
	clone.Salt = append([]byte(nil), envelope.Salt...)
	clone.Nonce = append([]byte(nil), envelope.Nonce...)
	clone.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	clone.Verifier = append([]byte(nil), envelope.Verifier...)
	return &clone
}

func testBytes(size int, value byte) []byte {
	return bytes.Repeat([]byte{value}, size)
}
