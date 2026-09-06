import assert from 'node:assert/strict'
import test from 'node:test'

globalThis.window = {
  location: {
    origin: 'https://money.example.test',
    pathname: '/'
  },
  dispatchEvent() {}
}

const { deleteTransaction } = await import('../src/utils/api.js')

test('deleteTransaction rejects unsuccessful HTTP responses', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'delete rejected' }), {
    status: 503,
    headers: { 'Content-Type': 'application/json' }
  })

  await assert.rejects(deleteTransaction(42), /delete rejected/)
})

test('deleteTransaction resolves after a successful HTTP response', async () => {
  globalThis.fetch = async () => new Response(null, { status: 204 })

  await deleteTransaction(42)
})
