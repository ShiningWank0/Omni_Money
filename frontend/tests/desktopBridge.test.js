import assert from 'node:assert/strict'
import test from 'node:test'
import { initializeDesktopBridge } from '../src/runtime/desktop-bridge.js'

test('server UI never loads native bindings or changes desktop detection', async () => {
  const target = {}
  const loaded = await initializeDesktopBridge({ document: { documentElement: { dataset: {} } }, window: target,
    bindings: () => { throw new Error('Native runtime loaded in the product server') } })
  assert.equal(loaded, false)
  assert.equal(target.go, undefined)
})
test('the native marker initializes the v3 binding adapter before application modules load', async () => {
  const target = {}, status = { unlocked: false }
  const loaded = await initializeDesktopBridge({ document: { documentElement: { dataset: { omniDesktop: 'wails3' } } },
    window: target, bindings: async () => ({ GetDesktopVaultStatus: async () => status }) })
  assert.equal(loaded, true)
  assert.equal(await target.go.main.App.GetDesktopVaultStatus(), status)
  assert.ok(Object.isFrozen(target.go.main.App))
})
test('incomplete native bindings fail without falling back to server APIs', async () => {
  const target = {}
  await assert.rejects(initializeDesktopBridge({ document: { documentElement: { dataset: { omniDesktop: 'wails3' } } },
    window: target, bindings: async () => ({}) }), /binding is unavailable/)
  assert.equal(target.go, undefined)
})
