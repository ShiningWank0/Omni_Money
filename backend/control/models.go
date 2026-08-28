// Package control owns the encrypted server control-plane database.
//
// Financial records never belong in this database. In particular, callers
// must use UserSummary for administrative user listings: password material,
// vault-key envelopes, and vault locations are deliberately separate.
package control

import (
	"time"

	"omni_money/backend/keyenvelope"
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type UserState string

const (
	UserActive   UserState = "active"
	UserDisabled UserState = "disabled"
)

type InvitationState string

const (
	InvitationPending  InvitationState = "pending"
	InvitationAccepted InvitationState = "accepted"
	InvitationRevoked  InvitationState = "revoked"
	InvitationExpired  InvitationState = "expired"
)

type RecoveryEnvelopeState string

const (
	RecoveryEnvelopeActive  RecoveryEnvelopeState = "active"
	RecoveryEnvelopeRevoked RecoveryEnvelopeState = "revoked"
)

type PasswordResetState string

const (
	PasswordResetPending  PasswordResetState = "pending"
	PasswordResetConsumed PasswordResetState = "consumed"
	PasswordResetRevoked  PasswordResetState = "revoked"
	PasswordResetExpired  PasswordResetState = "expired"
)

// UserSummary is the only model returned by user-listing operations. It must
// remain free of vault paths, financial metadata, salts, verifiers, and key
// envelopes so an admin endpoint cannot expose those values accidentally.
type UserSummary struct {
	ID          string
	Email       string
	DisplayName string
	Role        Role
	State       UserState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastLoginAt *time.Time
}

// PasswordCredentialInput contains opaque outputs from the authentication and
// vault-key wrapping layer. The control store never derives or logs them.
type PasswordCredentialInput struct {
	Envelope keyenvelope.Envelope
}

type PasswordCredential struct {
	UserID    string
	Envelope  keyenvelope.Envelope
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RecoveryEnvelopeInput wraps the same random vault key independently of the
// account password. Possession of the recovery code is required to unwrap it;
// an administrator cannot use this record alone to read a user's vault.
type RecoveryEnvelopeInput struct {
	ID       string
	Envelope keyenvelope.Envelope
}

type RecoveryEnvelope struct {
	ID        string
	UserID    string
	Envelope  keyenvelope.Envelope
	State     RecoveryEnvelopeState
	CreatedAt time.Time
	RevokedAt *time.Time
}

type NewUserInput struct {
	ID                 string
	Email              string
	DisplayName        string
	VaultID            string
	PasswordCredential PasswordCredentialInput
	RecoveryEnvelope   *RecoveryEnvelopeInput
}

type BootstrapAdminInput = NewUserInput

type CreateInvitationInput struct {
	ID        string
	Email     string
	Role      Role
	TokenHash []byte
	ExpiresAt time.Time
}

// Invitation intentionally excludes TokenHash. Lookup accepts the hash as an
// argument, but responses are safe for ordinary control-plane state handling.
type Invitation struct {
	ID         string
	Email      string
	Role       Role
	State      InvitationState
	CreatedBy  string
	AcceptedBy *string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ResolvedAt *time.Time
}

type CreatePasswordResetTicketInput struct {
	ID        string
	TokenHash []byte
	ExpiresAt time.Time
}

// PasswordResetTicket does not carry the bearer-token hash.
type PasswordResetTicket struct {
	ID         string
	UserID     string
	State      PasswordResetState
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ResolvedAt *time.Time
}
