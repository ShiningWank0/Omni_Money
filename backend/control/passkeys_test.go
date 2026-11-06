package control

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/keyenvelope"
)

func TestPasskeyCreationWaitsForWriterAndRechecksLimit(t *testing.T) {
	for _, atLimit := range []bool{false, true} {
		name := "available"
		if atLimit {
			name = "limit-reached-while-waiting"
		}
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t)
			admin := bootstrapTestAdmin(t, store)
			ctx := context.Background()
			if atLimit {
				for index := range MaxPasskeysPerUser - 1 {
					if _, err := store.CreatePasskeyCredential(ctx, testPasskeyInput(admin.ID, "existing", byte(index+1)), testNow); err != nil {
						t.Fatal(err)
					}
				}
			}
			blocker, err := store.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			if _, err := blocker.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer blocker.ExecContext(ctx, "ROLLBACK")
			if atLimit {
				_, credential, envelope, err := preparePasskeyCredential(testPasskeyInput(admin.ID, "competing", 60))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := blocker.ExecContext(ctx, `INSERT INTO passkey_credentials
					(credential_id,user_id,name,credential_json,prf_salt,vault_envelope_json,revision,created_at_ms,updated_at_ms)
					VALUES(?,?,?,?,?,?,1,?,?)`, bytes.Repeat([]byte{60}, 32), admin.ID, "competing", credential,
					bytes.Repeat([]byte{62}, 32), envelope, testNow.UnixMilli(), testNow.UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				_, err := store.CreatePasskeyCredential(ctx, testPasskeyInput(admin.ID, "waiting", 80), testNow)
				done <- err
			}()
			received := false
			defer func() {
				_, _ = blocker.ExecContext(ctx, "ROLLBACK")
				if !received {
					select {
					case <-done:
					case <-time.After(7 * time.Second):
						t.Error("passkey registration did not finish after rollback")
					}
				}
			}()
			select {
			case err := <-done:
				received = true
				t.Fatalf("registration returned before competing writer committed: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := blocker.ExecContext(ctx, "COMMIT"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				received = true
				if atLimit && !errors.Is(err, ErrConflict) || !atLimit && err != nil {
					t.Fatalf("registration error = %v, atLimit=%v", err, atLimit)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("registration did not finish after competing writer committed")
			}
			var count int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM passkey_credentials WHERE user_id=?", admin.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if atLimit {
				want = MaxPasskeysPerUser
			}
			if count != want {
				t.Fatalf("passkeys=%d, want %d", count, want)
			}
		})
	}
}

// Cancelling an immediate operation must release its writer and connection.
func TestImmediateControlTransactionCancellationReturnsConnection(t *testing.T) {
	store := openTestStore(t)
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	err := store.withImmediate(ctx, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "INSERT INTO login_throttle(account_key,last_failure_ms,updated_at_ms) VALUES(?,1,1)", strings.Repeat("a", 64)); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("operation error = %v", err)
	}
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	var count int
	if err := store.db.QueryRowContext(check, "SELECT COUNT(*) FROM login_throttle").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled operation left rows or connection: count=%d, err=%v", count, err)
	}
}

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

func TestPasswordRequiredPasskeyRoundTripsWithoutVaultEnvelope(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	input := testPasskeyInput(admin.ID, "Bitwarden", 31)
	input.PasswordRequired = true
	input.VaultEnvelope = keyenvelope.Envelope{}
	created, err := store.CreatePasskeyCredential(context.Background(), input, testNow)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetPasskeyCredential(context.Background(), admin.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.PasswordRequired || !loaded.Summary().PasswordRequired || loaded.VaultEnvelope.Kind != "" {
		t.Fatalf("password-required passkey did not round-trip: %+v", loaded.Summary())
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

func TestLegacyPasskeyEnvelopeUpgradeUsesCASAndCannotResurrectDeletion(t *testing.T) {
	store := openTestStore(t)
	admin := bootstrapTestAdmin(t, store)
	ctx := context.Background()
	input := testPasskeyInput(admin.ID, "Bitwarden", 51)
	input.PasswordRequired = true
	record, err := store.CreatePasskeyCredential(ctx, input, testNow)
	if err != nil {
		t.Fatal(err)
	}
	binding := keyenvelope.Context{UserID: admin.ID, VaultID: "vault-binding"}
	envelope, err := keyenvelope.WrapWithServerPasskey(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), binding, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePasskeyEnvelope(ctx, record, *envelope, testNow); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.GetPasskeyCredential(ctx, admin.ID, record.ID)
	if err != nil || upgraded.PasswordRequired || upgraded.VaultEnvelope.Kind != keyenvelope.KindServerPasskey || upgraded.Revision != record.Revision+1 {
		t.Fatalf("upgrade failed: %v", err)
	}
	if err := store.ReplacePasskeyEnvelope(ctx, record, *envelope, testNow); !errors.Is(err, ErrCredentialConflict) {
		t.Fatal("stale upgrade succeeded")
	}
	if err := store.DeletePasskeyCredential(ctx, admin.ID, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePasskeyEnvelope(ctx, upgraded, *envelope, testNow); !errors.Is(err, ErrCredentialConflict) {
		t.Fatal("deleted credential was resurrected")
	}
}
