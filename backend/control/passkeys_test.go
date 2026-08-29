package control

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/keyenvelope"
)

func testPasskeyInput(userID, name string, seed byte) PasskeyCredentialInput {
	id := bytes.Repeat([]byte{seed}, 32)
	return PasskeyCredentialInput{
		UserID: userID,
		Name:   name,
		Credential: webauthn.Credential{
			ID: id, PublicKey: bytes.Repeat([]byte{seed + 1}, 77),
			Authenticator: webauthn.Authenticator{SignCount: 1},
		},
		PRFSalt: bytes.Repeat([]byte{seed + 2}, keyenvelope.PasskeySecretSize),
		VaultEnvelope: keyenvelope.Envelope{
			Version: keyenvelope.CurrentVersion, Kind: keyenvelope.KindPasskey, KDF: passkeyEnvelopeKDF,
			Salt: bytes.Repeat([]byte{seed + 3}, keyenvelope.SaltSize), Nonce: bytes.Repeat([]byte{seed + 4}, 12),
			Ciphertext: bytes.Repeat([]byte{seed + 5}, keyenvelope.DEKSize+16),
		},
	}
}

func TestPasskeyCredentialLifecycleAndCAS(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	ctx := context.Background()

	created, err := store.CreatePasskeyCredential(ctx, testPasskeyInput(admin.ID, "MacBook Touch ID", 21), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Summary().Name != "MacBook Touch ID" {
		t.Fatalf("unexpected created passkey: %#v", created)
	}

	listed, err := store.ListPasskeyCredentials(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].Credential, created.Credential) ||
		!reflect.DeepEqual(listed[0].VaultEnvelope, created.VaultEnvelope) {
		t.Fatalf("passkey did not round-trip: %#v", listed)
	}

	updated := listed[0].Credential
	updated.Authenticator.SignCount = 2
	usedAt := testNow.Add(2 * time.Minute)
	if err := store.RecordSuccessfulPasskeyUse(ctx, listed[0], updated, usedAt, false); err != nil {
		t.Fatal(err)
	}
	if user, err := store.GetUser(ctx, admin.ID); err != nil || user.LastLoginAt != nil {
		t.Fatalf("passkey reauthentication changed last login: %#v, %v", user.LastLoginAt, err)
	}
	if err := store.RecordSuccessfulPasskeyUse(ctx, listed[0], updated, usedAt, false); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("stale credential revision error = %v, want ErrCredentialConflict", err)
	}

	refreshed, err := store.GetPasskeyCredential(ctx, admin.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Revision != 2 || refreshed.Credential.Authenticator.SignCount != 2 || refreshed.LastUsedAt == nil || !refreshed.LastUsedAt.Equal(usedAt) {
		t.Fatalf("unexpected refreshed passkey: %#v", refreshed)
	}
	updated = refreshed.Credential
	updated.Authenticator.SignCount = 3
	loginAt := usedAt.Add(time.Minute)
	if err := store.RecordSuccessfulPasskeyUse(ctx, refreshed, updated, loginAt, true); err != nil {
		t.Fatal(err)
	}
	if user, err := store.GetUser(ctx, admin.ID); err != nil || user.LastLoginAt == nil || !user.LastLoginAt.Equal(loginAt) {
		t.Fatalf("passkey login time not recorded: %#v, %v", user.LastLoginAt, err)
	}
	if err := store.DeletePasskeyCredential(ctx, admin.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPasskeyCredential(ctx, admin.ID, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted passkey lookup error = %v, want ErrNotFound", err)
	}
}

func TestPasskeySummaryCannotExposeVaultMaterial(t *testing.T) {
	typeInfo := reflect.TypeOf(PasskeySummary{})
	for _, forbidden := range []string{"credential", "public", "salt", "envelope", "vault", "key"} {
		for index := 0; index < typeInfo.NumField(); index++ {
			if bytes.Contains([]byte(strings.ToLower(typeInfo.Field(index).Name)), []byte(forbidden)) {
				t.Fatalf("PasskeySummary exposes forbidden field %q", typeInfo.Field(index).Name)
			}
		}
	}
}
