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
    snapshots/*.db                 encrypted with the same vault key
```

The control database contains identities, roles, account state, one-way token
digests, password-verification material, and encrypted vault-key envelopes. It
does not contain balances, account names, transactions, receipt images, or a raw
vault key.

Each vault has an independent random 256-bit data-encryption key (DEK). The
server control key never wraps a vault DEK. A user's password-derived key and
recovery-derived key wrap the DEK independently with AES-256-GCM. The envelope
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

## Administration boundary

The first server administrator is created only through an explicit bootstrap
flow protected by a secret read from `INITIAL_ADMIN_SETUP_TOKEN_FILE`. A fresh
public server must never make the first unauthenticated HTTP caller an
administrator.

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
2. first-admin bootstrap, invitations, login/recovery/reset, request-scoped vault selection, and legacy single-user migration;
3. per-user AI credentials bound cryptographically to their owner vault;
4. the desktop single-user vault setup, unlock, recovery, and lock lifecycle.

The first stage exposes no multi-user HTTP endpoints. Issue #62 remains open until
the server and desktop flows are complete and verified on the latest `main`.
