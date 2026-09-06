import { expect, it, vi } from 'vitest'

it('fails an unmocked network request immediately and creates disposable state', async () => {
  await expect(fetch('https://unmocked.invalid/test')).rejects.toThrow(
    'Unexpected network request in component test: https://unmocked.invalid/test'
  )
  localStorage.setItem('component-leak', 'secret')
  sessionStorage.setItem('component-leak', 'secret')
  vi.stubGlobal('__componentLeak', true)
  window.addEventListener('component-leak', () => { globalThis.__componentListenerCalled = true }, { capture: true })
  vi.useFakeTimers()
})

it('restores storage, globals, listeners and timers between tests', () => {
  window.dispatchEvent(new Event('component-leak'))
  expect(globalThis.__componentListenerCalled).toBeUndefined()
  expect(localStorage.getItem('component-leak')).toBeNull()
  expect(sessionStorage.getItem('component-leak')).toBeNull()
  expect(globalThis.__componentLeak).toBeUndefined()
  expect(vi.isFakeTimers()).toBe(false)
})

it('does not let real timers from one test fire in the next test', async () => {
  setTimeout(() => { globalThis.__componentRealTimeoutCalled = true }, 25)
  window.setTimeout(() => { globalThis.__componentWindowTimeoutCalled = true }, 25)
  setInterval(() => { globalThis.__componentRealIntervalCalled = true }, 25)
  window.setInterval(() => { globalThis.__componentWindowIntervalCalled = true }, 25)
})

it('keeps real timers from a previous test cleared', async () => {
  await new Promise(resolve => setTimeout(resolve, 75))
  expect(globalThis.__componentRealTimeoutCalled).toBeUndefined()
  expect(globalThis.__componentWindowTimeoutCalled).toBeUndefined()
  expect(globalThis.__componentRealIntervalCalled).toBeUndefined()
  expect(globalThis.__componentWindowIntervalCalled).toBeUndefined()
})
