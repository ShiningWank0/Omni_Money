import assert from 'node:assert/strict'
import test from 'node:test'
import { createPinia, setActivePinia } from 'pinia'

const failure = new Error('hydrate failed')
globalThis.window = {
  go: { main: { App: { GetAccounts: async () => { throw failure } } } },
  location: { origin: 'wails://wails', pathname: '/' },
  dispatchEvent() {},
  atob: globalThis.atob,
  btoa: globalThis.btoa
}

const { enableDesktopFinancialCalls } = await import('../src/utils/api.js')
const { useAppStore } = await import('../src/store/index.js')

test('store fetches can propagate hydration failures without changing default UI behavior', async () => {
  setActivePinia(createPinia())
  const store = useAppStore()
  enableDesktopFinancialCalls()
  const originalError = console.error
  console.error = () => {}
  try {
    await assert.rejects(store.fetchAccounts({ throwOnError: true }), failure)
    await assert.doesNotReject(store.fetchAccounts())
  } finally {
    console.error = originalError
  }
  assert.deepEqual(store.accounts, [])
})
