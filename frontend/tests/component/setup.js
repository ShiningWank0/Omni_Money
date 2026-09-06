import { enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, vi } from 'vitest'

enableAutoUnmount(afterEach)

let listenerCleanup = []
let timerCleanup = []

function trackGlobalListeners(target) {
  const add = target.addEventListener.bind(target)
  const remove = target.removeEventListener.bind(target)
  const listeners = []
  const capture = options => typeof options === 'boolean' ? options : Boolean(options?.capture)

  vi.spyOn(target, 'addEventListener').mockImplementation((type, listener, options) => {
    listeners.push({ type, listener, options })
    return add(type, listener, options)
  })
  vi.spyOn(target, 'removeEventListener').mockImplementation((type, listener, options) => {
    const index = listeners.findIndex(candidate =>
      candidate.type === type && candidate.listener === listener &&
      capture(candidate.options) === capture(options)
    )
    if (index >= 0) listeners.splice(index, 1)
    return remove(type, listener, options)
  })

  return () => {
    for (const { type, listener, options } of listeners.splice(0)) remove(type, listener, options)
  }
}

function trackGlobalTimers(target) {
  const setTimeout = target.setTimeout.bind(target)
  const clearTimeout = target.clearTimeout.bind(target)
  const setInterval = target.setInterval.bind(target)
  const clearInterval = target.clearInterval.bind(target)
  const timeouts = new Set()
  const intervals = new Set()

  vi.spyOn(target, 'setTimeout').mockImplementation((...args) => {
    const handle = setTimeout(...args)
    timeouts.add(handle)
    return handle
  })
  vi.spyOn(target, 'clearTimeout').mockImplementation(handle => {
    timeouts.delete(handle)
    clearTimeout(handle)
  })
  vi.spyOn(target, 'setInterval').mockImplementation((...args) => {
    const handle = setInterval(...args)
    intervals.add(handle)
    return handle
  })
  vi.spyOn(target, 'clearInterval').mockImplementation(handle => {
    intervals.delete(handle)
    clearInterval(handle)
  })

  return () => {
    for (const handle of timeouts) clearTimeout(handle)
    for (const handle of intervals) clearInterval(handle)
    timeouts.clear()
    intervals.clear()
  }
}

beforeEach(() => {
  listenerCleanup = [trackGlobalListeners(window), trackGlobalListeners(document)]
  timerCleanup = [trackGlobalTimers(globalThis)]
  if (window !== globalThis) timerCleanup.push(trackGlobalTimers(window))
  vi.stubGlobal('fetch', vi.fn(async input => {
    throw new Error(`Unexpected network request in component test: ${String(input)}`)
  }))
})

afterEach(() => {
  for (const cleanup of listenerCleanup.splice(0)) cleanup()
  vi.useRealTimers()
  for (const cleanup of timerCleanup.splice(0)) cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  window.localStorage.clear()
  window.sessionStorage.clear()
})
