package serverauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"omni_money/backend/control"
	"omni_money/backend/keyenvelope"
)

// This floor masks ordinary differences between missing-user and credential
// queries. It is not a constant-time guarantee under database overload. The
// public HTTP rate limit still applies before this work starts.
const passkeyLoginResponseFloor = 100 * time.Millisecond

func (s *Service) beginPrivatePasskeyLogin(ctx context.Context, email, clientKey string) (result PasskeyLoginBegin, resultErr error) {
	started := time.Now()
	defer func() {
		timer := time.NewTimer(time.Until(started.Add(passkeyLoginResponseFloor)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			result, resultErr = PasskeyLoginBegin{}, ctx.Err()
		case <-timer.C:
		}
	}()
	email, err := normalizeLoginEmail(email)
	if err != nil {
		return PasskeyLoginBegin{}, ErrInvalidCredentials
	}
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, control.ErrNotFound) {
		return PasskeyLoginBegin{}, err
	}
	// Always perform a credential query, including for missing and disabled
	// accounts. The synthetic identity is never eligible to complete a login.
	lookupID := user.ID
	if errors.Is(err, control.ErrNotFound) {
		lookupID = "decoy_" + base64.RawURLEncoding.EncodeToString(s.passkeyDecoy(email, "user", 0)[:32])
	}
	records, err := s.passkeyStore.ListPasskeyCredentials(ctx, lookupID)
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	if user.State != control.UserActive || len(records) == 0 {
		user, records = control.UserSummary{}, nil
	}
	if len(records) > control.MaxPasskeysPerUser {
		return PasskeyLoginBegin{}, ErrServiceUnavailable
	}

	descriptors := make([]protocol.CredentialDescriptor, 0, control.MaxPasskeysPerUser)
	prf := make(map[string]any, control.MaxPasskeysPerUser)
	ownedIDs := make([][]byte, 0, len(records))
	for _, record := range records {
		if len(record.ID) == 0 || !bytes.Equal(record.ID, record.Credential.ID) || len(record.PRFSalt) != keyenvelope.PasskeySecretSize {
			return PasskeyLoginBegin{}, ErrServiceUnavailable
		}
		descriptors = append(descriptors, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: record.ID})
		ownedIDs = append(ownedIDs, record.ID)
		prf[base64.RawURLEncoding.EncodeToString(record.ID)] = map[string]any{"first": protocol.URLEncodedBase64(record.PRFSalt)}
	}
	for index := len(records); index < control.MaxPasskeysPerUser; index++ {
		id := s.passkeyDecoyCredentialID(email, index)
		salt := s.passkeyDecoy(email, "prf", index)[:keyenvelope.PasskeySecretSize]
		descriptors = append(descriptors, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: id})
		prf[base64.RawURLEncoding.EncodeToString(id)] = map[string]any{"first": protocol.URLEncodedBase64(salt)}
	}
	// A stable order and stable decoys prevent repeated requests from exposing
	// which entries are real. Omit authenticator transports for every entry.
	sort.Slice(descriptors, func(i, j int) bool {
		return bytes.Compare(descriptors[i].CredentialID, descriptors[j].CredentialID) < 0
	})
	assertion, session, err := s.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAssertionExtensions(protocol.AuthenticationExtensions{"prf": map[string]any{"evalByCredential": prf}}),
	)
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	assertion.Response.AllowedCredentials = descriptors
	// Padding is a browser hint only. Server verification must retain exactly
	// the real, owned credential IDs; go-webauthn checks all of them for ownership.
	session.UserID = []byte(user.ID)
	session.AllowedCredentialIDs = ownedIDs
	ceremonyID, err := s.storePasskeyCeremony(passkeyCeremony{
		Kind: passkeyCeremonyLogin, UserID: user.ID, ClientKey: clientKey, Session: *session,
	})
	if err != nil {
		return PasskeyLoginBegin{}, err
	}
	return PasskeyLoginBegin{CeremonyID: ceremonyID, Options: assertion}, nil
}

func (s *Service) passkeyDecoy(email, purpose string, index int) []byte {
	mac := hmac.New(sha512.New, s.passkeyPrivacyKey[:])
	_, _ = fmt.Fprintf(mac, "%s\x00%d\x00%s", purpose, index, email)
	return mac.Sum(nil)
}

// Real authenticator IDs have varying lengths. A single fixed dummy length
// would make every other length an immediate existence oracle. Cover common
// lengths and the full WebAuthn range, deterministically per email and slot.
// This is a mitigation, not a claim that arbitrary authenticator formats or
// their statistical length distribution can be hidden by an allowlist.
func (s *Service) passkeyDecoyCredentialID(email string, index int) []byte {
	seed := s.passkeyDecoy(email, "credential", index)
	length := 16 + (int(seed[0])*256+int(seed[1]))%1008
	commonLengths := [...]int{16, 32, 64, 96, 128, 256}
	if choice := int(seed[2]) % (len(commonLengths) + 1); choice < len(commonLengths) {
		length = commonLengths[choice]
	}
	xof := sha3.NewSHAKE256()
	_, _ = xof.Write(seed)
	result := make([]byte, length)
	_, _ = xof.Read(result)
	return result
}
