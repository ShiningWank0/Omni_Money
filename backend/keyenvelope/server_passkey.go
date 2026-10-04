package keyenvelope

import (
	"encoding/binary"
	"fmt"
)

// KindServerPasskey is server-managed key custody, separate from PRF envelopes.
// A server operator with the custody key and control database can decrypt it.
const KindServerPasskey Kind = "passkey-server"

func serverPasskeyAAD(binding Context, credentialID []byte) ([]byte, error) {
	if err := validateContext(binding); err != nil {
		return nil, err
	}
	credentialLength := len(credentialID)
	if credentialLength < 16 || credentialLength > 1024 {
		return nil, ErrInvalidContext
	}
	aad := authenticatedData(binding, KindServerPasskey, CurrentVersion)
	aad = binary.BigEndian.AppendUint32(aad, uint32(credentialLength))
	return append(aad, credentialID...), nil
}

func WrapWithServerPasskey(dek, custodyKey []byte, binding Context, credentialID []byte) (*Envelope, error) {
	if err := validateDEK(dek); err != nil {
		return nil, err
	}
	if err := validatePasskeySecret(custodyKey); err != nil {
		return nil, err
	}
	aad, err := serverPasskeyAAD(binding, credentialID)
	if err != nil {
		return nil, err
	}
	defer clear(aad)
	salt, err := randomBytes(SaltSize)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(custodyKey, salt, hkdfInfo(KindServerPasskey, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	nonce, ciphertext, err := seal(key, dek, aad)
	if err != nil {
		return nil, err
	}
	return &Envelope{Version: CurrentVersion, Kind: KindServerPasskey, KDF: passkeyKDF,
		Salt: salt, Nonce: nonce, Ciphertext: ciphertext}, nil
}

// UnwrapWithServerPasskey must be called only after verifying the registered
// credential's assertion. Public credential IDs and signatures are never keys.
func UnwrapWithServerPasskey(envelope *Envelope, custodyKey []byte, binding Context, credentialID []byte) ([]byte, error) {
	if err := validateCommonEnvelope(envelope, KindServerPasskey, passkeyKDF); err != nil {
		return nil, err
	}
	if envelope.Profile != (Argon2idProfile{}) || len(envelope.Verifier) != 0 {
		return nil, fmt.Errorf("%w: server passkey envelope contains password metadata", ErrInvalidEnvelope)
	}
	if err := validatePasskeySecret(custodyKey); err != nil {
		return nil, ErrAuthentication
	}
	aad, err := serverPasskeyAAD(binding, credentialID)
	if err != nil {
		return nil, err
	}
	defer clear(aad)
	key, err := deriveKey(custodyKey, envelope.Salt, hkdfInfo(KindServerPasskey, "aead"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	dek, err := open(key, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil || len(dek) != DEKSize {
		clear(dek)
		return nil, ErrAuthentication
	}
	return dek, nil
}
