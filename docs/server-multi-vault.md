# Server multi-vault security model

Omni Money server separates service administration from access to financial data.
An application administrator can manage accounts, invitations, disabled state, and
server settings, but does not receive another user's vault key and cannot use an
administration API to open another user's ledger.

Both Desktop and multi-user server production modes currently provide no AI
capability. The production server returns 404 for `/api/v1/ai/*` and
`/api/ai-console/*`, and rejects legacy AI environment variables at startup.
Per-user AI is Stage 4 planned/unshipped; dormant source packages are not a
production feature.

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

Sessions and one-use WebAuthn ceremonies live in server memory, not the control
database. A server restart invalidates sessions; the durable save queue lives in
each encrypted user ledger and survives independently.

Each vault has an independent random 256-bit data-encryption key (DEK). A user's
password-derived key and recovery-derived key wrap the DEK with AES-256-GCM.
Passkey registration uses the already authenticated request's open vault key;
the browser neither supplies nor receives a plaintext DEK. Standard passkeys
use a persistent custody key derived with a dedicated HKDF purpose from the
control key. Custody envelopes authenticate the user ID, vault ID, credential
ID, purpose, and version. Existing PRF envelopes remain readable for migration.

Server-managed passkeys change the offline protection boundary: a server operator
with the control key and control database can recover the DEK of an account with
a custody envelope. This is not a zero-knowledge mode. Application administrators
still have no API for selecting or opening another user's financial vault.

## Password and recovery derivation

Password derivation uses one fixed, versioned Argon2id profile. Metadata selects
an allow-listed profile ID instead of supplying arbitrary memory or iteration
values. Purpose-specific keys are then derived with HKDF-SHA-256 so an
authentication verifier cannot be reused as a vault-wrapping key.

Recovery uses an independently generated 256-bit secret shown to the user once.
Only a verifier and an AES-GCM envelope are stored. Without a usable passkey, password, or recovery secret, users cannot open their
vault. An application administrator cannot bypass recovery through a reset API.
Server operators with custody material have the separate offline capability
described above.

An administrator-initiated password reset therefore creates a short-lived,
single-use reset ticket. The user must also present the recovery secret to keep
the old vault. Without it, the old vault remains encrypted and is not silently
deleted or replaced.

## Passkey authentication

Registration requires an authenticated vault session and a WebAuthn creation
ceremony with user verification. It does not request an account password or a
second PRF assertion. New registrations store a server-custody envelope and
work with standard providers, including providers that return no PRF output.
The original password login remains available.

Login verifies the signature, challenge, RP ID, origin, user handle, and user
verification before unwrapping the credential-bound custody envelope. Ceremony
state is one-use, short-lived, stored only in memory, and bound to the requesting
client address; registration ceremonies also bind the initiating session.
Counters are committed with compare-and-swap and clone warnings fail
verification. Reauthentication needs only a valid assertion for the current
account and never needs to decrypt a vault again.

Legacy PRF and password-required credentials are upgraded with compare-and-swap
after a successful password login or when opening passkey settings in an already
unlocked session. No user input or re-registration is needed for server-registered
credentials. A passkey saved only in a provider after a previously failed
registration has no server record and must be registered again.

Control-plane listings expose only credential ID, name, legacy password-required
status, and timestamps. Public keys, PRF salts, counters, and encrypted DEKs
remain server-side.

Ordinary logout revokes its session family synchronously, including concurrent
reauthentication rotations, and deletes its cookie before responding. Root
cleanup runs in the background; already admitted writes keep their own request
lease until they finish. Freshly authenticated logins prove the same DEK and can
reuse the live instance during that work. Automatic snapshot cleanup does not
block a new session; the final database close is serialized before any fresh
instance can open. Explicit credential-revocation, disable, restore, and shutdown
drains remain fail-closed and cannot be adopted by a new login.

The browser purges private UI state immediately on logout and rejects late API
responses. Before revoking the session it waits for outstanding transaction-save
receipts, then navigates after the revocation response. An unconfirmed upload or
revocation keeps the screen locked and offers a retry.

## Durable transaction-save receipts

Server-mode transaction create/update uses `/api/transaction-saves`. Each user
save intent has a UUID, and retrying that operation preserves both its UUID and
immutable payload. The request also binds the authenticated user ID; a rotated
cookie cannot send an old user's pending input into another user's vault.
Identical contents with different IDs remain separate transactions. Reusing an
ID with different contents is rejected with 409. Receipt IDs are scoped to the
user vault, and completed/failed tombstones remain for safe replay.

The complete input, including image uploads, is committed to the user's
SQLCipher ledger with FULL synchronous durability before returning 202. Image
decoding, validation, ledger mutations, and automatic snapshots run afterward
on an instance-owned worker. Logout waits for receipt confirmation only; it
does not wait for that processing. Each ledger mutation and its completed
receipt are committed in one SQL transaction. A disconnect or logout cannot
cancel accepted work. Closing the vault joins its worker before destroying the
key. A pending receipt after a process crash resumes when that user's vault is
next unlocked, because the worker needs the vault DEK.

Schema version 6 adds this queue. The ordinary HTTP body limit remains 10 MiB
including Base64, and the browser limits new image data to 7 MiB total. These are
separate from the internal queue bounds. Each stored input is bounded to 32 MiB, with at most
32 retained inputs and 128 MiB per vault, including unacknowledged failed input.
Only the owner can query processing notices or dismiss a failed notice. Failed
input stays encrypted until explicitly dismissed; dismissal keeps the receipt
ID/hash tombstone. Notices expose text metadata without downloading image data.
The browser shows pending/failed notices after login and refreshes the ledger
after pending work completes.

CSV v3 exports ledger contents, not save receipts, retained failed input, or
replay tombstones. Schema-6 snapshots and whole-data-root archives include the
queue as part of the ledger database. Restoring an earlier snapshot also rolls
back receipts and replay records created after that snapshot.

There is no persistent offline browser outbox. Unconfirmed upload input is held
in memory for retry, so logout cannot silently declare success before receipt.
Closing/reloading the browser before receipt confirmation can lose that local
input. CSV imports and the other synchronous ledger APIs retain their existing
request behavior; this receipt protocol covers the transaction editor's create
and update operations.

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
- an administrator password or application administrator session cannot open another user's vault through an API; server custody keys provide the offline capability described above.

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
off-host encrypted backup, not a plaintext export.

Successful ledger mutation paths exposed through the vault-bound `core.Service`
schedule an asynchronous snapshot. Bursts are coalesced into at most one
follow-up run. The common manual/automatic create path prunes to 30 generations
and `SNAPSHOT_MAX_TOTAL_BYTES` before returning, and the automatic worker also
runs a follow-up cleanup. This is mutation-triggered automation, not a
configurable wall-clock schedule; there is no product UI for timing, retention
policy, or failure notification. An application Admin/API cannot list, decrypt,
or restore another user's plaintext snapshots, but the same service UID, host
root/operator, replaceable binary, and process memory remain inside the hosting
trust boundary.

Restore is a high-impact operation requiring CSRF and recent reauthentication.
The exact restore route first authenticates the current user without borrowing
the request child lease, then obtains a root-only manager capability. That
capability drains every session for that user, waits for in-flight requests,
atomically validates and swaps the candidate database, and closes/zeroizes the
instance. A restore never affects another user's vault. The browser is forced
to log in again after either a successful restore or a failed restore attempt.
An application administrator may manage accounts, but cannot list, decrypt, or
restore another user's snapshots through the application boundary.

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
4. per-user AI credentials bound cryptographically to their owner vault
   (planned/unshipped; production AI remains disabled);
5. the desktop single-user vault setup, unlock, recovery, and lock lifecycle.

## Snapshot restore drill

An encrypted snapshot alone is not a DR set. Back up the control DB and control
key, each vault and its encrypted snapshots, volume key/recovery material and
attestation/restore procedure, and each user's recovery code to separate safe
locations. `scripts/backup-data-root.sh` automates the cold whole-data-root
archive (tree contract validation, member validation, sha256 sidecar,
plaintext-header rejection, manifest, `--verify` mode); see
[disaster-recovery.md](disaster-recovery.md) for the storage trust model,
restore procedure, and drill steps. During a drill, copy the complete set to an isolated data root,
start the server without exposing it, and verify a
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
