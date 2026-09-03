import assert from 'node:assert/strict'
import test from 'node:test'

let resolveAccounts
globalThis.window = {
  go: {
    main: {
      App: {
        GetAccounts: () => new Promise(resolve => { resolveAccounts = resolve }),
        GetDesktopVaultStatus: async () => ({ state: 'locked', unlocked: false }),
        LockDesktopVault: async () => ({ state: 'locked', unlocked: false })
      }
    }
  },
  location: { origin: 'wails://wails', pathname: '/' },
  dispatchEvent() {},
  atob: globalThis.atob,
  btoa: globalThis.btoa
}

const {
  DesktopVaultResponseInvalidatedError,
  enableDesktopFinancialCalls,
  getAccounts,
  getDesktopVaultStatus,
  invalidateDesktopFinancialCalls,
  lockDesktopVault
} = await import('../src/utils/api.js')

test('desktop financial responses from a prior unlocked generation are discarded', async () => {
  enableDesktopFinancialCalls()
  const pending = getAccounts()
  invalidateDesktopFinancialCalls()
  resolveAccounts(['private account'])
  await assert.rejects(pending, DesktopVaultResponseInvalidatedError)
  await assert.rejects(getAccounts(), DesktopVaultResponseInvalidatedError)
})

test('desktop lifecycle status and lock calls remain available while financial calls are disabled', async () => {
  assert.deepEqual(await getDesktopVaultStatus(), { state: 'locked', unlocked: false })
  assert.deepEqual(await lockDesktopVault(), { state: 'locked', unlocked: false })
})
