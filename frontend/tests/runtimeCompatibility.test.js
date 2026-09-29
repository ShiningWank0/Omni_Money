import test from 'node:test'
import { execFileSync } from 'node:child_process'

test('Pinia state updates work in WebViews without Object.hasOwn', () => {
  // Use a separate process so emulating an older WebView does not change
  // built-ins used by the test runner or other tests.
  execFileSync(process.execPath, ['--input-type=module', '-e', `
    import assert from 'node:assert/strict'
    delete Object.hasOwn
    await import('./src/runtime/compatibility.js')
    const { createPinia, defineStore } = await import('pinia')

    const store = defineStore('compatibility', {
      state: () => ({ account: { name: 'Before', balance: 100 } }),
    })(createPinia())
    store.$patch({ account: { balance: 200 } })
    assert.equal(store.account.balance, 200)
    assert.equal(store.account.name, 'Before')

    const key = Symbol('key')
    const record = Object.assign(Object.create(null), { hasOwnProperty: false, [key]: 1 })
    assert.equal(Object.hasOwn(record, key), true)
    assert.equal(Object.hasOwn(record, 'hasOwnProperty'), true)
    assert.equal(Object.hasOwn({}, 'toString'), false)
    assert.throws(() => Object.hasOwn(null, 'key'), TypeError)
    assert.equal(Object.getOwnPropertyDescriptor(Object, 'hasOwn').enumerable, false)
  `], { cwd: new URL('../', import.meta.url), stdio: 'pipe' })
})

test('modern WebViews retain their native Object.hasOwn', () => {
  execFileSync(process.execPath, ['--input-type=module', '-e', `
    import assert from 'node:assert/strict'
    const nativeHasOwn = Object.hasOwn
    await import('./src/runtime/compatibility.js')
    assert.equal(Object.hasOwn, nativeHasOwn)
  `], { cwd: new URL('../', import.meta.url), stdio: 'pipe' })
})
