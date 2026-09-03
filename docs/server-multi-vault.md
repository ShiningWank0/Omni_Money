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
    snapshots/<timestamp>.db       SQLCipher ciphertext, opened only with the same vault key
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

Users can rotate their password or recovery code after proving the current password. Both operations unwrap the DEK only inside the authenticated account service and rewrap the unchanged DEK; the control store commits an exact-envelope/revision compare-and-swap. Password rotation explicitly chooses whether passkeys remain valid or are deleted in the same transaction. Successful credential revocation invalidates all sessions before the Vault manager begins draining that user's leases. Individual and bulk passkey revocation use the same session-and-vault shutdown boundary.

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
- credential change, account disable, role change, and reset revoke sessions before closing the affected vault;
- the last active administrator cannot be disabled or demoted;
- an administrator cannot disable their own current account;
- invitation and reset tokens are random, short-lived, single-use, and stored only as digests;
- administrative token lists expose IDs, state, subject, and timestamps only; token values, digests, and envelopes are never returned;
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

Per-user snapshots are exposed only through the authenticated user's bound
request lease. `GET` and `POST /api/snapshots` never accept a user ID, vault ID,
path, or directory and return basenames only. Each snapshot is made with the
same per-vault SQLCipher DEK as `ledger.db`; it is ciphertext suitable for an
off-host encrypted backup, not a plaintext export. Retention is bounded by 30
generations and `SNAPSHOT_MAX_TOTAL_BYTES`.

Restore is a high-impact operation requiring CSRF and recent reauthentication.
The exact restore route first authenticates the current user without borrowing
the request child lease, then obtains a root-only manager capability. That
capability drains every session for that user, waits for in-flight requests,
atomically validates and swaps the candidate database, and closes/zeroizes the
instance. A restore never affects another user's vault. The browser is forced
to log in again after either a successful restore or a failed restore attempt.
An application administrator may manage accounts, but cannot list, decrypt, or
restore another user's snapshots.

## Threat boundary

This design protects stopped databases, snapshots, copied files, and the product's
application-level administrator boundary. It also prevents accidental cross-user
database selection in normal server handlers.

The snapshot threat model assumes a single writer per service and UID. Ciphertext
replay or replacement by that same service UID is outside this boundary.

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
   route selection, account-only admin UI, and authenticated per-user snapshots;
4. per-user AI credentials bound cryptographically to their owner vault;
5. the desktop single-user vault setup, unlock, recovery, and lock lifecycle.

## Snapshot restore drill

Back up only the encrypted `snapshots/` files to an off-host encrypted backup
system. During a drill, copy the ciphertext and the user's recovery material
to an isolated data root, start the server without exposing it, and verify a
known transaction, image, tag, `PRAGMA integrity_check`, current schema
migration, and critical indexes/triggers. Exercise a corrupt file, a wrong-key
file, and a rollback failure. Keep the original snapshot unchanged. After a
restore, all sessions are revoked and the user must log in again; record the
drill time without logging keys, paths, or financial contents.

The app-admin boundary does not protect a host root or a process that can read
the server's memory: an operator able to replace the binary, inspect memory,
or capture a password can obtain a live DEK. SQLCipher snapshots, encrypted
off-host storage, least privilege, and host-volume encryption protect data at
rest within the application threat model, not a compromised host.
