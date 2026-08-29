# Server multi-vault security model

Omni Money server separates service administration from access to financial data.
An application administrator can manage accounts, invitations, disabled state, and
server settings, but does not receive another user's vault key and cannot use an
administration API to open another user's ledger.

## Storage layout

```text
data/
  control/omni_control.db          SQLCipher, opened with the server control key
  vaults/<opaque-vault-id>/
    ledger.db                      SQLCipher, opened with that user's vault key
```

The control database contains identities, roles, account state, one-way token
digests, password-verification material, and encrypted vault-key envelopes. It
does not contain balances, account names, transactions, receipt images, or a raw
vault key.

Each vault has an independent random 256-bit data-encryption key (DEK). The
server control key never wraps a vault DEK. A user's password-derived key,
recovery-derived key, and each registered WebAuthn PRF output wrap the DEK
independently with AES-256-GCM. The envelope
authenticates the user ID, vault ID, purpose, and format version as associated
data, so moving an envelope to another account or vault fails authentication.

## Password and recovery derivation

Password derivation uses one fixed, versioned Argon2id profile. Metadata selects
an allow-listed profile ID instead of supplying arbitrary memory or iteration
values. Purpose-specific keys are then derived with HKDF-SHA-256 so an
authentication verifier cannot be reused as a vault-wrapping key.

Recovery uses an independently generated 256-bit secret shown to the user once.
Only a verifier and an AES-GCM envelope are stored. Losing both the password and
recovery secret makes the existing vault unrecoverable; an administrator cannot
bypass that property by resetting the account.

An administrator-initiated password reset therefore creates a short-lived,
single-use reset ticket. The user must also present the recovery secret to keep
the old vault. Without it, the old vault remains encrypted and is not silently
deleted or replaced.

## Passkey authentication

Passkeys are an alternative login path, not a replacement for password or recovery. Registration first verifies the current password, performs a WebAuthn ceremony with user verification required, and requires the authenticator's PRF extension. A credential-specific random salt produces a 32-byte PRF result; a purpose-separated HKDF key derived from that result wraps the unchanged vault DEK. The PRF result and plaintext DEK are never stored.

Login begins after normalizing the supplied email so the server can send only that user's allowed credential IDs and credential-specific PRF salts. A signed WebAuthn assertion, required user verification, the correct PRF result, and the matching envelope are all needed before the Vault opens. The same assertion and envelope proof can satisfy recent reauthentication without creating a second Vault session. Ceremony state is one-use, short-lived, stored only in memory, and bound to the requesting client address. Authenticator counters are updated with a compare-and-swap transaction, and clone warnings fail authentication.

Control-plane listings expose only a base64url credential ID, user-chosen name, creation time, and last-use time. Public keys, PRF salts, counters, and wrapped Vault keys remain server-side. Password login remains available after registration, and recovery still requires the separately saved recovery code.

## Administration boundary

The first server administrator is created only through an explicit bootstrap
flow protected by a secret read from `INITIAL_ADMIN_SETUP_TOKEN_FILE`. A fresh
public server must never make the first unauthenticated HTTP caller an
administrator. Compose deployments supply it only through
`compose.bootstrap.yaml`, then recreate the service without that overlay and
retire the token after the first administrator is confirmed.

Administrators create invitations, not user passwords or vault keys. The invited
user chooses the password and receives the recovery secret while their vault is
provisioned. Administration list responses deliberately omit password material,
key envelopes, vault paths, and financial metadata.

The following invariants apply:

- an administrator's normal financial APIs select only the administrator's own vault;
- account disable and reset revoke sessions and close the affected vault;
- the last active administrator cannot be disabled;
- an administrator cannot disable their own current account;
- invitation and reset tokens are random, short-lived, single-use, and stored only as digests;
- a server request receives its database instance from the authenticated principal; financial APIs do not accept a caller-supplied user ID;
- the control key, an administrator password, or an administrator session alone cannot open another user's vault.

A live server session owns one root vault lease. Each authenticated request
borrows a child lease and receives only a guarded business service, never the
raw database instance. Logging out, disabling the account, expiry, eviction, or
server shutdown releases the root. Releasing the last root immediately blocks
new borrows, waits for requests already in flight, then closes the SQLCipher
database and destroys its in-memory opener key.

## Production startup and HTTP boundary

The production server no longer accepts the legacy `DB_PATH`,
`DB_ENCRYPTION_KEY_FILE`, `AUTH_PASSWORD_HASH`, or TOTP environment settings.
It requires these process-level values before opening a listener:

- `CONTROL_DB_PATH`: SQLCipher control database below the attested data root;
- `CONTROL_DB_ENCRYPTION_KEY_FILE`: an independent 32-byte control key stored outside the data root;
- `VAULT_ROOT`: a dedicated real directory below the same attested data root;
- `INITIAL_ADMIN_SETUP_TOKEN_FILE`: required only while the control database has no users;
- `DATA_AT_REST_MODE` and `DATA_AT_REST_ATTESTATION_FILE`;
- the existing session, host, proxy, and HTTPS settings.

On a fresh deployment the login page generates a 32-byte recovery secret in
the browser, shows it before submission, and requires the operator to confirm
that it has been saved. Passwords, recovery secrets, bearer tokens, and the
setup token cross the JSON boundary as Base64-decoded byte fields. The server
clears its owned request buffers after each operation. The setup token is read
only on an unbootstrapped control database and its in-memory digest is destroyed
during shutdown.

Public account routes are exact method/path matches for status, bootstrap,
login, invitation acceptance, and password-reset completion. Every other API
route requires a server-side session. Financial handlers receive only the
request's guarded `core.Service`; absence of that service is a fixed 503 and
never falls back to the Desktop/global database. Admin actor IDs come only from
the refreshed authenticated user context, never a request field.

Per-user snapshot restore and AI delegation are not yet safely expressible
through the vault manager. Server routes report snapshots unavailable, omit the
AI console/listener, and startup rejects legacy AI environment settings. These
features remain disabled until they have manager-exclusive or user-bound
capabilities.

## Threat boundary

This design protects stopped databases, snapshots, copied files, and the product's
application-level administrator boundary. It also prevents accidental cross-user
database selection in normal server handlers.

It cannot protect an unlocked vault from an operating-system administrator or
malware that can replace the server binary, inspect process memory, inject code,
or capture a user's password. Defending against a malicious hosting operator
would require browser-side end-to-end encryption and a different trust model.
Host hardening, the existing encrypted-volume requirement, least privilege, and
encrypted off-host backups remain required layers.

## Delivery stages

The multi-vault change is intentionally split into reviewable stages:

1. database instances, key envelopes, encrypted control store, and an internal vault manager;
2. instance-bound business services, atomic credential/reset mutations, and
   session/request vault lease ownership;
3. first-admin HTTP bootstrap, invitations, login/recovery/reset, production
   route selection, and the account-only admin UI (delivered); legacy
   single-user migration remains follow-up work;
4. per-user AI credentials bound cryptographically to their owner vault;
5. the desktop single-user vault setup, unlock, recovery, and lock lifecycle.

The first stage exposes no multi-user HTTP endpoints. Issue #62 remains open until
the server and desktop flows are complete and verified on the latest `main`.
