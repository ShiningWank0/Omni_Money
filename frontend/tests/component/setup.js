import { enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, vi } from 'vitest'

enableAutoUnmount(afterEach)

let listenerCleanup = []

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

beforeEach(() => {
  listenerCleanup = [trackGlobalListeners(window), trackGlobalListeners(document)]
  vi.stubGlobal('fetch', vi.fn(async input => {
    throw new Error(`Unexpected network request in component test: ${String(input)}`)
  }))
})

afterEach(() => {
  for (const cleanup of listenerCleanup.splice(0)) cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  window.localStorage.clear()
  window.sessionStorage.clear()
})
