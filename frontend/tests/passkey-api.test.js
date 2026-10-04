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
const sourcePRFBase64 = () => window.btoa(String.fromCharCode(...sourcePRF))
const sourcePRFBase64url = () => sourcePRFBase64().replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const assertCredentialCarriesNoPRF = body => {
  assert.equal(body.credential.clientExtensionResults?.prf?.results, undefined)
  const encoded = JSON.stringify(body.credential)
  assert.equal(encoded.includes(sourcePRFBase64()), false)
  assert.equal(encoded.includes(sourcePRFBase64url()), false)
}
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
          toJSON: () => ({
            id: 'passkey-id',
            type: 'public-key',
            response: { signature: 'AA' },
            clientExtensionResults: { prf: { enabled: true, results: { first: sourcePRFBase64url() } } }
          }),
          getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: sourcePRF.buffer } } })
        }
      }
    }
  }
})

const { getAuthStatus, loginWithPasskey, registerPasskey, reauthenticateWithPasskey } = await import('../src/utils/api.js')

afterEach(() => {
  delete globalThis.fetch
  createCredential = null
})

test('passkey login starts discovery without sending an email', async () => {
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options })
    if (url === '/api/auth/passkeys/discover/begin') {
      assert.equal(options.body, undefined)
      return Response.json({ ceremony_id: 'discover', options: { publicKey: {
        challenge: 'AQID', allowCredentials: [], extensions: { prf: { eval: { first: 'CgsM' } } }
      } } })
    }
    if (url === '/api/auth/passkeys/discover/finish') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'discover')
      assertCredentialCarriesNoPRF(body)
      assert.equal(body.email, undefined)
      return Response.json({ authenticated: true, csrf_token: 'login-csrf' })
    }
    throw new Error(`unexpected request: ${url}`)
  }

  const result = await loginWithPasskey()
  assert.equal(result.authenticated, true)
  assert.deepEqual(requests.map(request => request.url), [
    '/api/auth/passkeys/discover/begin', '/api/auth/passkeys/discover/finish'
  ])
})

test('passkey login submits a provider assertion without PRF or password', async () => {
  const requests = []
  const priorGet = navigator.credentials.get
  navigator.credentials.get = async () => ({
    toJSON: () => ({ id: 'bitwarden-passkey', type: 'public-key', response: { signature: 'AA' } }),
    getClientExtensionResults: () => ({})
  })
  try {
    globalThis.fetch = async (url, options = {}) => {
      requests.push(url)
      if (url.endsWith('/begin')) return Response.json({ ceremony_id: 'login', options: { publicKey: { challenge: 'AQID' } } })
      const body = JSON.parse(options.body)
      assert.equal(body.prf_result_b64, '')
      assert.equal(body.password_b64, undefined)
      return Response.json({ authenticated: true, csrf_token: 'login-csrf' })
    }
    assert.equal((await loginWithPasskey()).authenticated, true)
    assert.deepEqual(requests, ['/api/auth/passkeys/discover/begin', '/api/auth/passkeys/discover/finish'])
  } finally { navigator.credentials.get = priorGet }
})

test('passkey registration finishes directly when creation returns a PRF result', async () => {
  createCredential = {
    toJSON: () => ({
      id: 'create-id',
      type: 'public-key',
      response: { attestationObject: 'AA' },
      clientExtensionResults: { prf: { enabled: true, results: { first: sourcePRFBase64url() } } }
    }),
    getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: sourcePRF.buffer } } })
  }
  const requests = []
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options })
    if (url === '/api/auth/passkeys/register/begin') {
      return Response.json({
        ceremony_id: 'register-ceremony',
        options: { publicKey: { challenge: 'AQID', user: { id: 'BAUG' }, extensions: { prf: {} } } }
      })
    }
    if (url === '/api/auth/passkeys/register/finish') {
      const body = JSON.parse(options.body)
      assert.equal(body.ceremony_id, 'register-ceremony')
      assert.equal(body.name, 'MacBook')
      assert.equal(body.credential.id, 'create-id')
      assertCredentialCarriesNoPRF(body)
      assert.equal(body.prf_result_b64, undefined)
      assert.equal(body.password_b64, undefined)
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

for (const extensions of [{}, { prf: { enabled: true } }]) {
  test(`registration completes with one creation prompt and no password: ${JSON.stringify(extensions)}`, async () => {
    createCredential = {
      toJSON: () => ({ id: 'create-id', type: 'public-key', response: {} }),
      getClientExtensionResults: () => extensions
    }
    const requests = []
    globalThis.fetch = async (url, options = {}) => {
      requests.push(url)
      if (url.endsWith('/begin')) return Response.json({ ceremony_id: 'registration', options: { publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } } })
      assert.equal(url, '/api/auth/passkeys/register/finish')
      const body = JSON.parse(options.body)
      assert.equal(body.password_b64, undefined)
      assert.equal(body.prf_result_b64, undefined)
      return Response.json({ passkey: { id: 'create-id', name: 'Bitwarden', password_required: false } })
    }
    assert.equal((await registerPasskey({ name: 'Bitwarden' })).passkey.password_required, false)
    assert.deepEqual(requests, ['/api/auth/passkeys/register/begin', '/api/auth/passkeys/register/finish'])
  })
}

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
      assertCredentialCarriesNoPRF(body)
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
