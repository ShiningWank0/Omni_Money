import assert from 'node:assert/strict'
import test from 'node:test'
import { panelApiReply } from './e2e/fixtures/panelApiReply.js'

const reads = { '/api/accounts': ['cash'], '/api/transactions': [] }

test('panel layout fixtures accept the real POST-only subscription sync contract', () => {
  const reply = panelApiReply('POST', '/api/subscription-sync', reads)
  assert.equal(reply.status, 200)
  assert.equal(reply.contentType, 'application/json')
  assert.deepEqual(JSON.parse(reply.body), { created: 0, has_more: false, errors: [] })
  assert.deepEqual(panelApiReply('POST', '/api/auth/keepalive', reads), { status: 204 })
  assert.deepEqual(JSON.parse(panelApiReply('GET', '/api/accounts', reads).body), ['cash'])
})

test('panel layout fixtures still reject unknown traffic and incorrect HTTP methods', () => {
  for (const [method, path] of [
    ['GET', '/api/subscription-sync'], ['PUT', '/api/subscription-sync'], ['DELETE', '/api/subscription-sync'],
    ['POST', '/api/accounts'], ['POST', '/api/transactions'], ['GET', '/api/auth/keepalive'],
    ['GET', '/api/unexpected'], ['POST', '/api/unexpected'], ['GET', '/api/toString'],
  ]) assert.equal(panelApiReply(method, path, reads), null, `${method} ${path} must remain unexpected`)
})
