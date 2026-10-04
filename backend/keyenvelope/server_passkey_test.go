package keyenvelope

import (
	"bytes"
	"testing"
)

func TestServerPasskeyEnvelopeAuthenticatesUserVaultCredentialAndCustodyKey(t *testing.T) {
	dek, custody := bytes.Repeat([]byte{11}, 32), bytes.Repeat([]byte{12}, 32)
	id := bytes.Repeat([]byte{13}, 32)
	binding := Context{UserID: "user-1", VaultID: "vault-1"}
	envelope, err := WrapWithServerPasskey(dek, custody, binding, id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(envelope.Ciphertext, dek) {
		t.Fatal("envelope contains plaintext DEK")
	}
	actual, err := UnwrapWithServerPasskey(envelope, custody, binding, id)
	if err != nil || !bytes.Equal(actual, dek) {
		t.Fatalf("round trip failed: %v", err)
	}
	clear(actual)
	for _, tc := range []struct {
		name    string
		binding Context
		id, key []byte
	}{
		{"user", Context{UserID: "user-2", VaultID: binding.VaultID}, id, custody},
		{"vault", Context{UserID: binding.UserID, VaultID: "vault-2"}, id, custody},
		{"credential", binding, bytes.Repeat([]byte{14}, 32), custody},
		{"key", binding, id, bytes.Repeat([]byte{15}, 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := UnwrapWithServerPasskey(envelope, tc.key, tc.binding, tc.id)
			if err == nil || len(actual) != 0 {
				clear(actual)
				t.Fatal("substituted binding decrypted envelope")
			}
		})
	}
	corrupt := *envelope
	corrupt.Ciphertext = bytes.Clone(envelope.Ciphertext)
	corrupt.Ciphertext[0] ^= 1
	if _, err := UnwrapWithServerPasskey(&corrupt, custody, binding, id); err == nil {
		t.Fatal("corrupt envelope decrypted")
	}
	if _, err := UnwrapWithPasskey(envelope, custody, binding); err == nil {
		t.Fatal("custody envelope accepted as PRF envelope")
	}
}
