package serverauth

import (
	"bytes"
	"encoding/base64"
	"testing"

	"omni_money/backend/control"
)

func TestGeneratedBearerPersistsOnlyEquivalentDigest(t *testing.T) {
	token, generatedHash, err := GenerateBearerToken()
	if err != nil {
		t.Fatal(err)
	}
	parsedHash, err := HashEncodedBearerToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generatedHash, parsedHash) {
		t.Fatal("generated and parsed bearer hashes differ")
	}
	if bytes.Contains(generatedHash, []byte(token)) {
		t.Fatal("persisted bearer digest contains the bearer token")
	}
}

func TestEncodedSecretsRequireCanonicalExactLengthBase64URL(t *testing.T) {
	raw := bytes.Repeat([]byte{0x7a}, bearerTokenBytes)
	canonical := base64.RawURLEncoding.EncodeToString(raw)
	validHash, err := HashEncodedBearerToken(canonical)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := control.HashBearerToken(raw)
	if err != nil || !bytes.Equal(validHash, wantHash) {
		t.Fatal("canonical bearer did not hash its decoded bytes")
	}
	for _, invalid := range []string{
		"",
		canonical + "=",
		canonical[:len(canonical)-1],
		canonical + "A",
		"+" + canonical[1:],
	} {
		if _, err := HashEncodedBearerToken(invalid); err == nil {
			t.Fatalf("invalid bearer %q was accepted", invalid)
		}
		if _, err := ParseRecoverySecret(invalid); err == nil {
			t.Fatalf("invalid recovery secret %q was accepted", invalid)
		}
	}
}
