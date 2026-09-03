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

export async function executeSnapshotRestore({
  name,
  restore,
  isDesktop,
  setRestoring,
  clearMessage,
  clearSecrets,
  purgeState,
  clearMarker,
  emit,
  createSessionExpiredEvent,
  dispatchSessionExpired,
  isOnLoginPage,
  redirectToLogin
}) {
  setRestoring(true)
  clearMessage()

  function completeDesktopRestore(failed = false) {
    setRestoring(false)
    // Keep restored before close: the parent may unmount this v-if component
    // on close, so post-restore work must start while it is still mounted.
    if (failed) {
      emit('restored', { failed: true })
    } else {
      emit('restored')
    }
    emit('close')
  }

  function expireServerSession(reason) {
    clearSecrets()
    // The marker is only a convenience for the next page load. A restricted
    // WebView may throw while resolving localStorage or removing the marker;
    // neither case may interrupt state purge or session expiry handling.
    try {
      clearMarker()
    } catch {
      // Best effort only; the remaining restore cleanup is mandatory.
    }
    purgeState()
    setRestoring(false)
    emit('close')
    const event = createSessionExpiredEvent(reason)
    dispatchSessionExpired(event)
    if (!event.defaultPrevented && !isOnLoginPage()) {
      redirectToLogin(reason)
    }
  }

  let restoreFailed = false
  try {
    await restore(name)
  } catch {
    restoreFailed = true
  }

  if (isDesktop) {
    if (restoreFailed) clearSecrets()
    purgeState()
    completeDesktopRestore(restoreFailed)
    return
  }

  expireServerSession(restoreFailed ? 'snapshot-restore-failed' : 'snapshot-restored')
}
