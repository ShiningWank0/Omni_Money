package serverauth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
	"omni_money/backend/middleware"
	"omni_money/backend/securedb"
)

// The required CI job sets OMNI_REQUIRE_SQLCIPHER_TESTS=1: an unavailable
// cipher must fail that job, rather than turn the persistence check into a skip.
func TestPasskeyCBORLegacyCredentialsSurviveEncryptedStoreRestart(t *testing.T) {
	for _, f := range legacyCBORFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			if err := os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "control.db")
			var databaseKey securedb.RawKey
			for i := range databaseKey {
				databaseKey[i] = 73
			}
			defer databaseKey.Destroy()
			store, err := control.Open(ctx, securedb.NewEncryptedOpener(databaseKey), path)
			if err != nil {
				if os.Getenv("OMNI_REQUIRE_SQLCIPHER_TESTS") != "1" && (errors.Is(err, securedb.ErrCipherUnavailable) || errors.Is(err, securedb.ErrCipherVersion) || errors.Is(err, securedb.ErrCipherProvider)) {
					t.Skipf("SQLCipher integration build is not active: %v", err)
				}
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			template, memory := seedLegacyCredential(t, f, false)
			password, err := memory.GetPasswordCredential(ctx, serverAuthTestUserID)
			if err != nil {
				t.Fatal(err)
			}
			recovery, err := keyenvelope.WrapWithRecovery(bytes.Repeat([]byte{37}, 32), bytes.Repeat([]byte{81}, 32), keyenvelope.Context{UserID: serverAuthTestUserID, VaultID: serverAuthTestVaultID})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.BootstrapFirstAdmin(ctx, control.BootstrapAdminInput{
				ID: serverAuthTestUserID, Email: "person@example.test", DisplayName: "Legacy CBOR", VaultID: serverAuthTestVaultID,
				PasswordCredential: control.PasswordCredentialInput{Envelope: password.Envelope},
				RecoveryEnvelope:   &control.RecoveryEnvelopeInput{Envelope: *recovery},
			}, serverAuthTestNow)
			if err != nil {
				t.Fatal(err)
			}
			old := memory.records[0]
			_, err = store.CreatePasskeyCredential(ctx, control.PasskeyCredentialInput{UserID: old.UserID, Name: f.Name, Credential: old.Credential, PRFSalt: old.PRFSalt, VaultEnvelope: old.VaultEnvelope}, serverAuthTestNow)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := securedb.RequireEncryptedHeader(path); err != nil {
				t.Fatal(err)
			}
			store, err = control.Open(ctx, securedb.NewEncryptedOpener(databaseKey), path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := store.GetPasskeyCredential(ctx, serverAuthTestUserID, old.ID)
			if err != nil || !bytes.Equal(loaded.Credential.PublicKey, fixtureBytes(t, f.PublicKey)) {
				t.Fatalf("stored legacy bytes changed across restart: %v", err)
			}
			opened := 0
			service, err := NewService(Dependencies{
				Store: store, WebAuthn: template.webauthn, PasskeyPrivacyKey: bytes.Repeat([]byte{71}, 32), PasskeyCustodyKey: bytes.Repeat([]byte{72}, 32),
				Sessions: &fakeSessionInvalidator{}, Vaults: &fakeVaultDrainer{},
				OpenSession: func(user control.UserSummary, vaultID string, key *securedb.RawKey) (*middleware.Session, error) {
					if user.ID != serverAuthTestUserID || vaultID != serverAuthTestVaultID || !bytes.Equal(key[:], bytes.Repeat([]byte{37}, 32)) {
						t.Fatal("wrong persisted vault key")
					}
					opened++
					return &middleware.Session{UserID: user.ID}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			begin, err := service.BeginDiscoverablePasskeyLogin(ctx, "client")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			session, err := service.FinishDiscoverablePasskeyLogin(ctx, FinishPasskeyLoginInput{CeremonyID: begin.CeremonyID, ClientKey: "client", CredentialJSON: legacyAssertion(t, begin, f, 1)}, now)
			if err != nil || session == nil {
				t.Fatalf("persisted login: %v", err)
			}
			user, err := store.GetUser(ctx, serverAuthTestUserID)
			if err != nil || user.LastLoginAt == nil {
				t.Fatalf("login timestamp: %v", err)
			}
			loginAt := *user.LastLoginAt
			reauth, err := service.BeginPasskeyReauthentication(ctx, serverAuthTestUserID, "client")
			if err != nil {
				t.Fatal(err)
			}
			input := FinishPasskeyLoginInput{CeremonyID: reauth.CeremonyID, ClientKey: "client", CredentialJSON: legacyAssertion(t, reauth, f, 2)}
			if err := service.FinishPasskeyReauthentication(ctx, serverAuthTestUserID, input, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := service.FinishPasskeyReauthentication(ctx, serverAuthTestUserID, input, now.Add(time.Second)); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("persisted reauth replay: %v", err)
			}
			loaded, err = store.GetPasskeyCredential(ctx, serverAuthTestUserID, old.ID)
			if err != nil || loaded.Credential.Authenticator.SignCount != 2 || loaded.Revision != 3 {
				t.Fatalf("credential counter/revision: %+v %v", loaded.Credential.Authenticator, err)
			}
			user, err = store.GetUser(ctx, serverAuthTestUserID)
			if err != nil || user.LastLoginAt == nil || !user.LastLoginAt.Equal(loginAt) || opened != 1 {
				t.Fatalf("reauthentication changed login state: sessions=%d err=%v", opened, err)
			}
		})
	}
}
