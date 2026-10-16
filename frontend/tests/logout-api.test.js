import assert from 'node:assert/strict'
import { test } from 'node:test'

globalThis.window = {
  location: { origin: 'https://money.example.test', pathname: '/' },
  dispatchEvent() {},
  btoa: value => Buffer.from(value, 'binary').toString('base64')
}

let moduleID = 0
async function freshAPI() {
  const api = await import(`../src/utils/api.js?logout-test=${moduleID++}`)
  globalThis.fetch = async () => Response.json({ authenticated: true, user: { id: 'owner' }, csrf_token: 'csrf' })
  await api.getAuthStatus()
  return api
}

function waitForAbort(signal) {
  return new Promise((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(new Error('request aborted')), { once: true })
  })
}

async function flushMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

test('a lost logout response succeeds when the server confirms the session is already revoked', async () => {
  const api = await freshAPI()
  const requests = []
  globalThis.fetch = async (url, options) => {
    requests.push(url)
    if (url === '/api/auth/logout') {
      assert.equal(new Headers(options.headers).get('X-CSRF-Token'), 'csrf')
      throw new TypeError('response lost after revocation')
    }
    assert.equal(url, '/api/auth/status')
    return Response.json({ authenticated: false })
  }
  await api.logout()
  assert.deepEqual(requests, ['/api/auth/logout', '/api/auth/status'])
  globalThis.fetch = async (_url, options) => {
    assert.equal(new Headers(options.headers).get('X-CSRF-Token'), null)
    return Response.json({ success: true })
  }
  await api.apiFetch('/api/probe', { method: 'POST' })
})

test('a delayed logout response finishes after confirmation instead of waiting for the request timeout', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const api = await freshAPI()
  let logoutSignal
  const requests = []
  globalThis.fetch = async (url, options) => {
    requests.push(url)
    if (url === '/api/auth/logout') {
      logoutSignal = options.signal
      return waitForAbort(options.signal)
    }
    assert.equal(url, '/api/auth/status')
    return Response.json({ authenticated: false })
  }
  const logout = api.logout()
  await flushMicrotasks()
  assert.deepEqual(requests, ['/api/auth/logout'])
  t.mock.timers.tick(500)
  await logout
  assert.equal(logoutSignal.aborted, true)
  await flushMicrotasks()
  assert.deepEqual(requests, ['/api/auth/logout', '/api/auth/status'])
})

test('an active session cannot be mistaken for a completed logout', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const api = await freshAPI()
  let finishLogout
  globalThis.fetch = async url => {
    if (url === '/api/auth/logout') return new Promise(resolve => { finishLogout = resolve })
    return Response.json({ authenticated: true, user: { id: 'owner' }, csrf_token: 'csrf' })
  }
  let finished = false
  const logout = api.logout().then(() => { finished = true })
  await flushMicrotasks()
  t.mock.timers.tick(500)
  await flushMicrotasks()
  assert.equal(finished, false)
  finishLogout(Response.json({ success: true }))
  await logout
})

test('a failed revocation with an active session remains retryable and retains its CSRF token', async () => {
  const api = await freshAPI()
  globalThis.fetch = async url => {
    if (url === '/api/auth/logout') throw new TypeError('offline before revocation')
    return Response.json({ authenticated: true, user: { id: 'owner' }, csrf_token: 'csrf' })
  }
  await assert.rejects(api.logout(), error => error.code === 'network_error')
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/auth/logout')
    assert.equal(new Headers(options.headers).get('X-CSRF-Token'), 'csrf')
    return Response.json({ success: true })
  }
  await api.logout()
})

test('unreadable or failed status responses cannot authorize logout completion', async () => {
  for (const response of [
    () => Response.json({ authenticated: false }, { status: 503 }),
    () => Response.json({ error: 'proxy failure' }),
    () => new Response('<html>proxy</html>'),
    () => { throw new TypeError('offline') }
  ]) {
    const api = await freshAPI()
    globalThis.fetch = async url => {
      if (url === '/api/auth/logout') throw new TypeError('response lost')
      return response()
    }
    await assert.rejects(api.logout(), error => error.code === 'network_error')
  }
})

test('a hanging confirmation has its own deadline and does not strand logout', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const api = await freshAPI()
  globalThis.fetch = async (url, options) => {
    if (url === '/api/auth/logout') throw new TypeError('offline')
    return waitForAbort(options.signal)
  }
  const logout = api.logout()
  const failure = assert.rejects(logout, error => error.code === 'network_error')
  await flushMicrotasks()
  t.mock.timers.tick(2000)
  await failure
})

test('a successful response cancels the delayed status check', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const api = await freshAPI()
  const requests = []
  globalThis.fetch = async url => { requests.push(url); return Response.json({ success: true }) }
  await api.logout()
  t.mock.timers.tick(10_000)
  await flushMicrotasks()
  assert.deepEqual(requests, ['/api/auth/logout'])
})
