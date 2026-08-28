import assert from 'node:assert/strict'
import test from 'node:test'

import {
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
  assert.equal(desktopVaultNeedsSetup({ state: 'locked', configured: true }), false)
})

test('desktop password policy is byte-based and requires confirmation', () => {
  assert.throws(() => validateNewDesktopPassword('short', 'short'), /12〜1024 bytes/)
  assert.throws(() => validateNewDesktopPassword('x'.repeat(1025), 'x'.repeat(1025)), /12〜1024 bytes/)
  assert.throws(() => validateNewDesktopPassword('long-enough-passphrase', 'different-passphrase'), /一致しません/)
  assert.doesNotThrow(() => validateNewDesktopPassword('長い長い長い長い', '長い長い長い長い'))
})
