import assert from 'node:assert/strict'
import { bounded } from './wails-dev-integration.mjs'

// Invoked only by the CI-owned native WebView driver. Every call uses the
// generated production binding and the real SQLCipher-backed App service.
export async function nativeLedgerRound(page, scenario, password) {
  const call = (method, ...args) => bounded(page.evaluate(async ({ method, args }) => {
    const app = window.go?.main?.App
    if (typeof app?.[method] !== 'function') throw new Error('Native ledger binding is missing')
    return await app[method](...args)
  }, { method, args }), 15_000, `Native ${method} binding did not complete`)
  const account = 'native-ci-ledger'
  const request = { account, date: '2026-01-02', time: '09:10', item: 'native-persistence-probe',
    type: 'income', amount: 1234, memo: 'CI fixture' }
  const read = () => call('GetTransactions', account, request.item)
  if (scenario === 'setup') {
    assert.deepEqual(await read(), [], 'The private vault must start with an empty ledger')
    const created = await call('AddTransaction', request)
    assert.ok(Number.isSafeInteger(created.id) && created.id > 0, 'Created transaction identity is invalid')
    assert.equal(created.amount_exact, '1234')
    await call('UpdateTransaction', created.id, { ...request, amount: 2345 })
    const updated = await read()
    assert.equal(updated.length, 1)
    assert.equal(updated[0].id, created.id)
    assert.equal(updated[0].amount_exact, '2345')
    assert.equal(updated[0].account, account)
    assert.match(updated[0].date, /^2026-01-02/)
    const history = await call('GetBalanceHistory')
    assert.equal(history.balances_exact[account].at(-1), '2345', 'Native balance graph differs from the saved ledger')
    await call('LockDesktopVault')
    assert.equal((await call('GetDesktopVaultStatus')).unlocked, false)
    await assert.rejects(read(), 'A locked native vault must reject ledger reads')
    assert.equal((await call('UnlockDesktopVault', password)).unlocked, true)
    assert.equal((await read())[0].amount_exact, '2345')
    return { created: true, updated: true, exactBalance: true, lockedReadDenied: true, unlocked: true }
  }
  assert.equal(scenario, 'reopen', 'Unknown native ledger scenario')
  const persisted = await read()
  assert.equal(persisted.length, 1, 'Restart lost the saved native transaction')
  assert.equal(persisted[0].amount_exact, '2345')
  assert.equal(persisted[0].account, account)
  await call('DeleteTransaction', persisted[0].id)
  assert.deepEqual(await read(), [], 'Deleted native transaction is still present')
  return { persistedAfterRestart: true, deleted: true }
}
