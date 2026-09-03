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
