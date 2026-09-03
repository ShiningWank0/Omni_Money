export function clearSnapshotRestoreMarker(storage) {
  try {
    const resolvedStorage = storage === undefined ? globalThis.localStorage : storage
    resolvedStorage.removeItem('snapshot_restored')
    return true
  } catch {
    // Storage can be unavailable in hardened/private WebViews. The restore
    // flow must still purge state and dispatch its expiry event.
    return false
  }
}

export function notifySnapshotRestoreCompletion(emit, failed = false) {
  if (failed) {
    emit('restored', { failed: true })
  } else {
    emit('restored')
  }
  // Keep this after restored: the parent may unmount this v-if component on
  // close, so post-restore work must be started while it is still mounted.
  emit('close')
}
