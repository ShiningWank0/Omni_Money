export const DESKTOP_IDLE_DEFAULT_MINUTES = 15
export const DESKTOP_IDLE_MIN_MINUTES = 5
export const DESKTOP_IDLE_MAX_MINUTES = 120
export const DESKTOP_IDLE_STORAGE_KEY = 'omni-money.desktop-idle-lock'

const STORAGE_VERSION = 1
const IMMEDIATE_ACTIVITY_TYPES = new Set(['pointerdown', 'keydown', 'touchstart'])
const THROTTLED_ACTIVITY_TYPES = new Set(['pointermove', 'wheel'])

export function isValidDesktopIdleMinutes(value) {
  return Number.isInteger(value) &&
    value >= DESKTOP_IDLE_MIN_MINUTES &&
    value <= DESKTOP_IDLE_MAX_MINUTES
}

export function loadDesktopIdleMinutes(storage) {
  try {
    const resolvedStorage = storage === undefined ? globalThis.localStorage : storage
    const parsed = JSON.parse(resolvedStorage.getItem(DESKTOP_IDLE_STORAGE_KEY))
    if (parsed?.version === STORAGE_VERSION && isValidDesktopIdleMinutes(parsed.minutes)) {
      return parsed.minutes
    }
  } catch {
    // Storage can be unavailable in hardened/private WebViews. The default is
    // deliberately usable without persistence.
  }
  return DESKTOP_IDLE_DEFAULT_MINUTES
}

export function saveDesktopIdleMinutes(minutes, storage) {
  if (!isValidDesktopIdleMinutes(minutes)) return false
  try {
    const resolvedStorage = storage === undefined ? globalThis.localStorage : storage
    resolvedStorage.setItem(DESKTOP_IDLE_STORAGE_KEY, JSON.stringify({
      version: STORAGE_VERSION,
      minutes
    }))
    return true
  } catch {
    return false
  }
}

export function createDesktopVaultLockRequest({
  showCurtain,
  invalidateResponses,
  nextTick,
  purge,
  lock,
  setLockedStatus,
  onFailure,
  onSettled
}) {
  let pending = null

  return function requestDesktopVaultLock(reason) {
    if (pending) return pending
    showCurtain(reason)
    invalidateResponses()
    pending = (async () => {
      try {
        await nextTick()
        purge()
        const status = await lock(reason)
        setLockedStatus(status)
      } catch (error) {
        onFailure(error, reason)
      } finally {
        onSettled(reason)
      }
    })().finally(() => {
      pending = null
    })
    return pending
  }
}

export function createDesktopIdleLock({
  document,
  performanceNow = () => performance.now(),
  wallNow = () => Date.now(),
  setTimer = (callback, delay) => setTimeout(callback, delay),
  clearTimer = timer => clearTimeout(timer),
  onCurtainChange = () => {},
  onExpired = () => {}
}) {
  let timeoutMs = DESKTOP_IDLE_DEFAULT_MINUTES * 60 * 1000
  let lastPerformance = 0
  let lastWall = 0
  let lastMovementAccepted = Number.NEGATIVE_INFINITY
  let timer = null
  let started = false
  let curtainVisible = false
  let expiryRequested = false

  function elapsed() {
    return Math.max(
      0,
      performanceNow() - lastPerformance,
      wallNow() - lastWall
    )
  }

  function setCurtain(visible) {
    if (curtainVisible === visible) return
    curtainVisible = visible
    onCurtainChange(visible)
  }

  function clearScheduledCheck() {
    if (timer === null) return
    clearTimer(timer)
    timer = null
  }

  function expire(reason) {
    if (expiryRequested) return true
    expiryRequested = true
    clearScheduledCheck()
    setCurtain(true)
    onExpired(reason)
    return true
  }

  function checkExpiry(reason = 'idle') {
    if (!started || expiryRequested) return expiryRequested
    if (elapsed() >= timeoutMs) return expire(reason)
    return false
  }

  function scheduleCheck() {
    clearScheduledCheck()
    if (!started || expiryRequested) return
    const remaining = Math.max(1, timeoutMs - elapsed())
    timer = setTimer(() => {
      timer = null
      if (!checkExpiry('idle')) scheduleCheck()
    }, remaining)
  }

  function recordActivity(event) {
    if (!started || expiryRequested || document.visibilityState !== 'visible' || event?.isTrusted !== true) return
    const type = event?.type
    if (!IMMEDIATE_ACTIVITY_TYPES.has(type) && !THROTTLED_ACTIVITY_TYPES.has(type)) return

    // A first click after expiry must lock, not silently start a fresh window.
    if (checkExpiry('idle')) return

    if (THROTTLED_ACTIVITY_TYPES.has(type)) {
      const now = performanceNow()
      if (Math.max(0, now - lastMovementAccepted) < 1000) return
      lastMovementAccepted = now
    }

    lastPerformance = performanceNow()
    lastWall = wallNow()
    scheduleCheck()
  }

  function handleVisibilityChange() {
    if (!started || expiryRequested) return
    if (document.visibilityState !== 'visible') {
      setCurtain(true)
      return
    }

    // Do not uncover restored content until the elapsed-time decision has
    // completed. Sleep advances the wall clock even where performance.now()
    // pauses; a backwards wall-clock adjustment cannot reduce monotonic time.
    if (!checkExpiry('resume')) {
      setCurtain(false)
      scheduleCheck()
    }
  }

  function start(minutes) {
    if (!isValidDesktopIdleMinutes(minutes)) {
      throw new RangeError('Desktop auto-lock must be an integer from 5 to 120 minutes')
    }
    stop()
    timeoutMs = minutes * 60 * 1000
    lastPerformance = performanceNow()
    lastWall = wallNow()
    lastMovementAccepted = Number.NEGATIVE_INFINITY
    expiryRequested = false
    started = true
    document.addEventListener('pointerdown', recordActivity, { passive: true })
    document.addEventListener('keydown', recordActivity, { passive: true })
    document.addEventListener('touchstart', recordActivity, { passive: true })
    document.addEventListener('pointermove', recordActivity, { passive: true })
    document.addEventListener('wheel', recordActivity, { passive: true })
    document.addEventListener('visibilitychange', handleVisibilityChange)
    if (document.visibilityState !== 'visible') setCurtain(true)
    scheduleCheck()
  }

  function stop() {
    clearScheduledCheck()
    if (started) {
      document.removeEventListener('pointerdown', recordActivity)
      document.removeEventListener('keydown', recordActivity)
      document.removeEventListener('touchstart', recordActivity)
      document.removeEventListener('pointermove', recordActivity)
      document.removeEventListener('wheel', recordActivity)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
    started = false
    // The owner may keep its curtain up for a separate lock transition. Reset
    // only our internal edge state so the next start can synchronize again.
    curtainVisible = false
  }

  return { start, stop, checkExpiry, recordActivity, handleVisibilityChange }
}
