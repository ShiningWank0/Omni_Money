import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

class TestPublicKeyCredential {}
globalThis.PublicKeyCredential = TestPublicKeyCredential
globalThis.window = {
  isSecureContext: true,
  PublicKeyCredential: TestPublicKeyCredential,
  atob: globalThis.atob,
  btoa: globalThis.btoa,
  location: { origin: 'https://money.example.test', pathname: '/' },
  dispatchEvent() {}
}

const sourcePRF = Uint8Array.from({ length: 32 }, (_, index) => 255 - index)
let createCredential = null
Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: {
    credentials: {
      async create() {
        return createCredential
      },
      async get() {
        return {
          toJSON: () => ({ id: 'passkey-id', type: 'public-key', response: { signature: 'AA' } }),
          getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: sourcePRF.buffer } } })
        }
      }
    }
  }
})

const { getAuthStatus, registerPasskey, reauthenticateWithPasskey } = await import('../src/utils/api.js')

afterEach(() => {
  delete globalThis.fetch
  createCredential = null
})

test('passkey registration finishes directly when creation returns a PRF result', async () => {
  createCredential = {
    toJSON: () => ({ id: 'create-id', type: 'public-key', response: { attestationObject: 'AA' } }),
    getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: sourcePRF.buffer } } })
  }
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options })
    if (url === '/api/auth/passkeys/register/begin') {
      return Response.json({
        ceremony_id: 'register-ceremony',
        options: { publicKey: { challenge: 'AQID', user: { id: 'BAUG' }, extensions: { prf: { eval: { first: 'CgsM' } } } } }
      })
    }
    if (url === '/api/auth/passkeys/register/finish') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'register-ceremony')
      assert.equal(body.name, 'MacBook')
      assert.equal(body.credential.id, 'create-id')
      assert.equal(Uint8Array.from(atob(body.prf_result_b64), character => character.charCodeAt(0)).byteLength, 32)
      return Response.json({ passkey: { id: 1, name: 'MacBook' } })
    }
    throw new Error(`unexpected request: ${url}`)
  }

  const result = await registerPasskey({ name: 'MacBook', password: 'correct horse battery staple' })
  assert.equal(result.passkey.name, 'MacBook')
  assert.deepEqual(requests.map(request => request.url), [
    '/api/auth/passkeys/register/begin',
    '/api/auth/passkeys/register/finish'
  ])
})

test('passkey registration runs a PRF assertion step-up when creation has no result', async () => {
  createCredential = {
    toJSON: () => ({ id: 'create-id', type: 'public-key', response: { attestationObject: 'AA' } }),
    getClientExtensionResults: () => ({ prf: { enabled: true } })
  }
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options })
    if (url === '/api/auth/passkeys/register/begin') {
      return Response.json({
        ceremony_id: 'register-ceremony',
        options: { publicKey: { challenge: 'AQID', user: { id: 'BAUG' }, extensions: { prf: { eval: { first: 'CgsM' } } } } }
      })
    }
    if (url === '/api/auth/passkeys/register/assert/begin') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'register-ceremony')
      assert.equal(body.credential.id, 'create-id')
      return Response.json({
        ceremony_id: 'assert-ceremony',
        options: { publicKey: { challenge: 'BwgJ', allowCredentials: [{ id: 'BwgJ', type: 'public-key' }], extensions: { prf: { evalByCredential: { BwgJ: { first: 'CgsM' } } } } } }
      })
    }
    if (url === '/api/auth/passkeys/register/assert/finish') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'assert-ceremony')
      assert.equal(body.name, 'Bitwarden')
      assert.equal(body.credential.id, 'passkey-id')
      assert.equal(Uint8Array.from(atob(body.prf_result_b64), character => character.charCodeAt(0)).byteLength, 32)
      return Response.json({ passkey: { id: 2, name: 'Bitwarden' } })
    }
    throw new Error(`unexpected request: ${url}`)
  }

  const result = await registerPasskey({ name: 'Bitwarden', password: 'correct horse battery staple' })
  assert.equal(result.passkey.name, 'Bitwarden')
  assert.deepEqual(requests.map(request => request.url), [
    '/api/auth/passkeys/register/begin',
    '/api/auth/passkeys/register/assert/begin',
    '/api/auth/passkeys/register/assert/finish'
  ])
})

test('passkey registration rejects unreported and unsupported PRF without finishing', async () => {
  for (const extensions of [{ prf: { enabled: false } }, {}]) {
    createCredential = {
      toJSON: () => ({ id: 'create-id', type: 'public-key', response: {} }),
      getClientExtensionResults: () => extensions
    }
    const requests = []
    globalThis.fetch = async (url, options = {}) => {
      requests.push({ url, options })
      if (url === '/api/auth/passkeys/register/begin') {
        return Response.json({
          ceremony_id: 'register-ceremony',
          options: { publicKey: { challenge: 'AQID', user: { id: 'BAUG' }, extensions: { prf: { eval: { first: 'CgsM' } } } } }
        })
      }
      throw new Error(`unexpected request: ${url}`)
    }
    await assert.rejects(registerPasskey({ name: 'Unsupported', password: 'correct horse battery staple' }), /PRF/)
    assert.deepEqual(requests.map(request => request.url), ['/api/auth/passkeys/register/begin'])
  }
})

test('passkey reauthentication rotates auth state through the two-step API', async () => {
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options })
    if (url === '/api/auth/status') {
      return Response.json({ authenticated: true, csrf_token: 'csrf-before' })
    }
    assert.equal(new Headers(options.headers).get('X-CSRF-Token'), 'csrf-before')
    if (url === '/api/auth/passkeys/reauth/begin') {
      return Response.json({
        ceremony_id: 'ceremony',
        options: { publicKey: { challenge: 'AQID', allowCredentials: [], extensions: { prf: { evalByCredential: {} } } } }
      })
    }
    if (url === '/api/auth/passkeys/reauth/finish') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'ceremony')
      assert.equal(body.credential.id, 'passkey-id')
      assert.equal(Uint8Array.from(atob(body.prf_result_b64), character => character.charCodeAt(0)).byteLength, 32)
      return Response.json({ authenticated: true, csrf_token: 'csrf-after' })
    }
    throw new Error(`unexpected request: ${url}`)
  }

  await getAuthStatus()
  const result = await reauthenticateWithPasskey()
  assert.equal(result.csrf_token, 'csrf-after')
  assert.deepEqual(requests.map(request => request.url), [
    '/api/auth/status',
    '/api/auth/passkeys/reauth/begin',
    '/api/auth/passkeys/reauth/finish'
  ])
})
