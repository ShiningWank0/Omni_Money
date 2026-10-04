import assert from 'node:assert/strict'
import { test } from 'node:test'

globalThis.window = {
  location: { origin: 'https://money.example.test', pathname: '/' },
  dispatchEvent() {},
  btoa: value => Buffer.from(value, 'binary').toString('base64')
}
const transaction = { id: 5, date: '2026-10-04', account: 'cash', item: 'lunch', type: 'expense', amount: 100 }
let moduleID = 0
async function freshAPI() {
  const api = await import(`../src/utils/api.js?save-test=${moduleID++}`)
  globalThis.fetch = async () => Response.json({ authenticated: true, user_id: 'owner', csrf_token: 'csrf' })
  await api.getAuthStatus()
  return api
}
const completed = id => Response.json({ request_id: id, state: 'completed', transaction })

test('logout waits for a durable receipt but does not wait for processing', async () => {
  const api = await freshAPI()
  let deliverReceipt, id
  const requests = []
  globalThis.fetch = async (url, options) => {
    requests.push(url)
    if (url === '/api/transaction-saves') {
      const body = JSON.parse(options.body)
      id = body.request_id
      assert.equal(body.user_id, 'owner')
      assert.equal(body.transaction.item, 'lunch')
      return await new Promise(resolve => { deliverReceipt = resolve })
    }
    if (url === '/api/auth/logout') return Response.json({ success: true })
    throw new Error('logout waited for background processing')
  }
  const save = api.addTransaction(transaction)
  const invalidated = assert.rejects(save, error => error.code === 'session_invalidated')
  const logout = api.logout()
  await Promise.resolve()
  assert.deepEqual(requests, ['/api/transaction-saves'])
  await assert.rejects(api.addTransaction(transaction), error => error.code === 'session_invalidated')
  deliverReceipt(Response.json({ request_id: id, state: 'pending' }, { status: 202 }))
  await logout
  await invalidated
  assert.deepEqual(requests, ['/api/transaction-saves', '/api/auth/logout'])
})

test('a lost receipt is retried with the identical ID and immutable payload', async () => {
  const api = await freshAPI()
  const bodies = []
  globalThis.fetch = async (_url, options) => {
    bodies.push(options.body)
    if (bodies.length === 1) throw new TypeError('response lost after commit')
    return completed(JSON.parse(options.body).request_id)
  }
  const result = await api.addTransaction(transaction)
  assert.equal(result.transaction.id, 5)
  assert.equal(bodies.length, 2)
  assert.equal(bodies[0], bodies[1])
})

test('separate saves with identical contents receive different operation IDs', async () => {
  const api = await freshAPI()
  const bodies = []
  globalThis.fetch = async (_url, options) => {
    const body = JSON.parse(options.body); bodies.push(body)
    return completed(body.request_id)
  }
  await api.addTransaction(transaction)
  await api.addTransaction(transaction)
  assert.notEqual(bodies[0].request_id, bodies[1].request_id)
  assert.deepEqual(bodies[0].transaction, bodies[1].transaction)
})

test('unconfirmed receipt stops logout and a retry retains the original operation', async () => {
  const api = await freshAPI()
  const bodies = []; let logoutCalls = 0
  globalThis.fetch = async (url, options) => {
    if (url === '/api/auth/logout') { logoutCalls++; return Response.json({ success: true }) }
    bodies.push(options.body)
    throw new TypeError('offline')
  }
  await assert.rejects(api.updateTransaction(5, transaction), error => error.code === 'network_error')
  await assert.rejects(api.logout(), error => error.code === 'save_receipt_unconfirmed')
  assert.equal(logoutCalls, 0)
  globalThis.fetch = async (url, options) => {
    if (url === '/api/auth/logout') { logoutCalls++; return Response.json({ success: true }) }
    bodies.push(options.body)
    return completed(JSON.parse(options.body).request_id)
  }
  await api.logout()
  assert.equal(logoutCalls, 1)
  assert.equal(new Set(bodies).size, 1)
  assert.equal(JSON.parse(bodies[0]).target_id, 5)
})

test('CSRF rotation refreshes the token without changing the operation ID', async () => {
  const api = await freshAPI()
  let attempts = 0, originalBody
  globalThis.fetch = async (url, options) => {
    if (url === '/api/auth/status') return Response.json({ authenticated: true, user_id: 'owner', csrf_token: 'new-csrf' })
    if (++attempts === 1) { originalBody = options.body; return Response.json({ error: 'CSRF' }, { status: 403 }) }
    assert.equal(options.body, originalBody)
    assert.equal(new Headers(options.headers).get('X-CSRF-Token'), 'new-csrf')
    return completed(JSON.parse(options.body).request_id)
  }
  await api.addTransaction(transaction)
  assert.equal(attempts, 2)
})

test('account switching cannot redirect an old pending save to another user', async () => {
  const api = await freshAPI()
  let posts = 0
  globalThis.fetch = async url => {
    if (url === '/api/auth/status') return Response.json({ authenticated: true, user_id: 'different-user', csrf_token: 'other-csrf' })
    posts++; return Response.json({ error: 'CSRF' }, { status: 403 })
  }
  await assert.rejects(api.addTransaction(transaction), error => error.code === 'save_account_mismatch')
  assert.equal(posts, 1)
})

test('queued validation failure is reported as failure rather than saved', async () => {
  const api = await freshAPI()
  globalThis.fetch = async (url, options) => url === '/api/auth/logout'
    ? Response.json({ success: true })
    : Response.json({ request_id: JSON.parse(options.body).request_id, state: 'failed', error: '画像形式が無効です' })
  await assert.rejects(api.addTransaction(transaction), error => error.code === 'save_failed')
  await api.logout()
})

test('save notices validate their envelope and dismiss only the specified ID', async () => {
  const api = await freshAPI()
  const id = '7616712d-b63e-4dfa-9fa2-707b6b3c0d9a'
  const notice = { request_id: id, state: 'failed', error: '画像形式が無効です', request: transaction }
  globalThis.fetch = async (url, options) => {
    if (options?.method === 'DELETE') {
      assert.equal(url, `/api/transaction-saves/${id}`)
      return Response.json({ success: true })
    }
    return Response.json({ saves: [notice] })
  }
  assert.deepEqual(await api.getTransactionSaveNotices(), [notice])
  await api.dismissFailedTransactionSave(id)
  globalThis.fetch = async () => Response.json({ saves: [{ state: 'completed' }] })
  await assert.rejects(api.getTransactionSaveNotices(), error => error.code === 'invalid_response')
})
