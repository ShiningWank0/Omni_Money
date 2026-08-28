export function isDesktopVaultUnlocked(status) {
  if (status?.state) return status.state === 'unlocked'
  return status?.unlocked === true
}

export function desktopVaultNeedsSetup(status) {
  if (status?.state) return status.state === 'needs_setup'
  return status?.configured === false && status?.legacy_migration_required !== true
}

export function desktopVaultNeedsLegacyMigration(status) {
  return status?.state === 'legacy_migration_required' || status?.legacy_migration_required === true
}

export function canConfirmDesktopRecoveryDelivery({ recoveryCode, recoverySaved, busy }) {
  return typeof recoveryCode === 'string' && recoveryCode.length > 0 && recoverySaved === true && busy !== true
}

export function validateNewDesktopPassword(password, confirmation) {
  const passwordBytes = new TextEncoder().encode(password).length
  if (passwordBytes < 12 || passwordBytes > 1024) {
    throw new Error('パスワードは12〜1024 bytesにしてください')
  }
  if (password !== confirmation) {
    throw new Error('パスワードが一致しません')
  }
}
