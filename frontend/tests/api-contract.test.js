import assert from 'node:assert/strict'
import { test } from 'node:test'

globalThis.window = {
  location: { origin: 'https://money.example.test', pathname: '/', replace() {} },
  btoa: value => Buffer.from(value, 'binary').toString('base64'),
  dispatchEvent() {}
}
globalThis.CustomEvent = class {
  constructor(type, init = {}) { this.type = type; this.detail = init.detail; this.defaultPrevented = false }
  preventDefault() { this.defaultPrevented = true }
}
const api = await import('../src/utils/api.js')
const tx = { id: 1, date: '2026-01-01', account: 'cash', fundItem: 'cash', item: 'coffee', type: 'expense', amount: 100, balance: -100, memo: '' }
const tag = { id: 1, name: 'food', parent_id: null, level: 1 }
const entity = { id: 'user-1' }
const cases = [
  ['getAccounts', [], ['cash']], ['getItems', [], ['coffee']], ['getTransactions', [], [tx]],
  ['addTransaction', [tx], { message: 'added', transaction: tx }], ['updateTransaction', [1, tx], { message: 'updated', transaction: tx }],
  ['deleteTransaction', [1], { message: 'deleted' }],
  ['getBalanceHistory', [], { accounts: ['cash'], dates: ['2026-01-01'], balances: { cash: [-100] } }],
  ['getBalanceHistoryFiltered', [['cash']], { accounts: [], dates: [], balances: {} }],
  ['getCreditCardSettings', [], ['card']], ['getBankAccountSettings', [], ['bank']],
  ['saveCreditCardSettings', [[]], { credit_card_items: [] }], ['saveBankAccountSettings', [[]], { bank_account_items: [] }],
  ['createSnapshot', [], { path: 'snapshot.db' }], ['listSnapshots', [], ['snapshot.db']], ['restoreSnapshot', ['snapshot.db'], { message: 'restored', login_required: true }],
  ['addTransactionImage', [1, {}], { id: 1, filename: 'receipt.png' }], ['getTransactionImagesPage', [1], { images: [{ id: 1, filename: 'receipt.png' }] }], ['deleteTransactionImage', [1, 1], { message: 'deleted' }],
  ['getTags', [], [tag]], ['createTag', ['food'], tag], ['createTagByPath', ['food'], tag], ['updateTag', [1, 'food'], { message: 'updated' }], ['deleteTag', [1], { message: 'deleted' }],
  ['getTagDeleteImpact', [1], { tag_id: 1, tag_name: 'food', descendant_count: 0, transaction_count: 1 }],
  ['getTransactionTags', [1], [tag]], ['addTransactionTags', [1, [1]], { message: 'added' }], ['removeTransactionTag', [1, 1], { message: 'deleted' }],
  ['getTagSummary', [], [{ tag_id: 1, tag_name: 'food', amount: 100, count: 1 }]],
  ['getTransactionLinks', [1], [tx]], ['addTransactionLink', [1, 2], { message: 'added' }], ['removeTransactionLink', [1, 2], { message: 'deleted' }],
  ['getAuthStatus', [], { authenticated: true }], ['login', ['a@test', 'password'], { authenticated: true }],
  ['listServerUsers', [], { users: [entity] }], ['listServerInvitations', [], { invitations: [entity] }], ['listServerPasswordResets', [], { password_resets: [entity] }],
  ['enableServerUser', ['user'], { success: true }], ['setServerUserRole', ['user', 'user'], { success: true }], ['disableServerUser', ['user'], { success: true }],
  ['revokeServerInvitation', ['id'], { success: true }], ['revokeServerPasswordReset', ['id'], { success: true }],
  ['createServerInvitation', [{ email: 'a@test' }], { invitation: entity, token: 'token' }], ['createServerPasswordReset', ['user'], { password_reset: entity, token: 'token' }]
]

test('HTTP wrappers accept actual success envelopes and reject errors and malformed success', async t => {
  for (const [name, args, payload] of cases) {
    await t.test(name, async () => {
      globalThis.fetch = async () => Response.json(payload)
      await api[name](...args)
      globalThis.fetch = async () => Response.json({ error: 'rejected', code: 'service_unavailable' }, { status: 503 })
      await assert.rejects(api[name](...args), error => error instanceof api.ApiError && error.status === 503 && error.retryable)
      globalThis.fetch = async () => Response.json({ error: 'proxy error disguised as success' })
      await assert.rejects(api[name](...args), error => error instanceof api.ApiError && error.code === 'invalid_response')
      globalThis.fetch = async () => new Response('<html>gateway</html>', { status: 200 })
      await assert.rejects(api[name](...args), error => error instanceof api.ApiError && error.code === 'invalid_response')
    })
  }
})

test('legacy null lists and empty balance normalize to safe empty collections', async () => {
  globalThis.fetch = async () => Response.json(null)
  assert.deepEqual(await api.getAccounts(), [])
  assert.deepEqual(await api.getTags(), [])
  globalThis.fetch = async () => Response.json({ accounts: null, dates: null, balances: {} })
  assert.deepEqual(await api.getBalanceHistory(), { accounts: [], dates: [], balances: {} })
})

test('keepalive accepts only 204 and retains best-effort no-navigation behavior', async () => {
  let navigations = 0
  window.dispatchEvent = () => { navigations++ }
  globalThis.fetch = async () => new Response(null, { status: 204 })
  assert.equal(await api.keepAlive(), 204)
  globalThis.fetch = async () => Response.json({ error: 'expired' }, { status: 401 })
  await assert.rejects(api.keepAlive(), error => error.status === 401)
  assert.equal(navigations, 0)
  globalThis.fetch = async () => Response.json({})
  await assert.rejects(api.keepAlive(), error => error.code === 'invalid_response')
})

test('credential response loss stays ambiguous while definitive rejection remains marked', async () => {
  const input = { currentPassword: 'current', newPassword: 'new', revokePasskeys: false }
  for (const status of [200, 503]) {
    globalThis.fetch = async () => new Response('broken', { status })
    await assert.rejects(api.changeServerPassword(input), error => error instanceof api.ApiError && error.definitiveResponse !== true)
  }
  globalThis.fetch = async () => Response.json({ error: 'bad password' }, { status: 400 })
  await assert.rejects(api.changeServerPassword(input), error => error.definitiveResponse === true)
  globalThis.fetch = async () => { throw new TypeError('network lost') }
  await assert.rejects(api.changeServerPassword(input), error => error.code === 'network_error' && error.definitiveResponse !== true)
})

test('428 retries a Blob once after reauthentication and keeps the raw response usable', async () => {
  const blob = new Blob(['csv'])
  let calls = 0
  window.dispatchEvent = event => { if (event.type === 'omni-money:reauth-required') event.detail.resolve() }
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.body, blob)
    calls++
    return calls === 1 ? Response.json({ error: 'reauth' }, { status: 428 }) : new Response('raw body')
  }
  const response = await api.apiFetch('/api/ai-console/analysis', { method: 'POST', body: blob })
  assert.equal(await response.text(), 'raw body')
  assert.equal(calls, 2)
})
