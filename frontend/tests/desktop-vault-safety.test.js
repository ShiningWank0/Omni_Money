import assert from 'node:assert/strict'
import test from 'node:test'

import {
  canConfirmDesktopRecoveryDelivery,
  desktopVaultNeedsLegacyMigration,
  desktopVaultNeedsSetup,
  isDesktopVaultUnlocked,
  validateNewDesktopPassword
} from '../src/utils/desktopVaultSafety.js'

test('desktop vault stays locked unless backend explicitly reports unlocked', () => {
  assert.equal(isDesktopVaultUnlocked(null), false)
  assert.equal(isDesktopVaultUnlocked({ state: 'locked', unlocked: false }), false)
  assert.equal(isDesktopVaultUnlocked({ state: 'error', unlocked: true }), false)
  assert.equal(isDesktopVaultUnlocked({ state: 'unlocked' }), true)
})

test('setup and legacy migration states remain distinct', () => {
  assert.equal(desktopVaultNeedsSetup({ state: 'needs_setup' }), true)
  assert.equal(desktopVaultNeedsSetup({ configured: false }), true)
  assert.equal(desktopVaultNeedsLegacyMigration({ state: 'legacy_migration_required' }), true)
  assert.equal(desktopVaultNeedsLegacyMigration({ legacy_migration_required: true }), true)
  assert.equal(desktopVaultNeedsSetup({ configured: false, legacy_migration_required: true }), false)
  assert.equal(desktopVaultNeedsSetup({ state: 'legacy_migration_required', configured: false }), false)
  assert.equal(desktopVaultNeedsSetup({ state: 'locked', configured: true }), false)
})

test('a recovery candidate cannot be committed until the code is saved and no operation is running', () => {
  assert.equal(canConfirmDesktopRecoveryDelivery({ recoveryCode: '', recoverySaved: true, busy: false }), false)
  assert.equal(canConfirmDesktopRecoveryDelivery({ recoveryCode: 'one-time-code', recoverySaved: false, busy: false }), false)
  assert.equal(canConfirmDesktopRecoveryDelivery({ recoveryCode: 'one-time-code', recoverySaved: true, busy: true }), false)
  assert.equal(canConfirmDesktopRecoveryDelivery({ recoveryCode: 'one-time-code', recoverySaved: true, busy: false }), true)
})

test('desktop password policy is byte-based and requires confirmation', () => {
	  assert.throws(() => validateNewDesktopPassword('short', 'short'), /12〜1024 bytes/)
	  assert.doesNotThrow(() => validateNewDesktopPassword('x'.repeat(257), 'x'.repeat(257)))
	  assert.doesNotThrow(() => validateNewDesktopPassword('x'.repeat(1024), 'x'.repeat(1024)))
	  assert.throws(() => validateNewDesktopPassword('x'.repeat(1025), 'x'.repeat(1025)), /12〜1024 bytes/)
	  assert.throws(() => validateNewDesktopPassword('long-enough-passphrase', 'different-passphrase'), /一致しません/)
	  assert.doesNotThrow(() => validateNewDesktopPassword('長長長長', '長長長長'))
	  assert.throws(() => validateNewDesktopPassword('長'.repeat(342), '長'.repeat(342)), /12〜1024 bytes/)
})
