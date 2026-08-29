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
Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: {
    credentials: {
      async get() {
        return {
          toJSON: () => ({ id: 'passkey-id', type: 'public-key', response: { signature: 'AA' } }),
          getClientExtensionResults: () => ({ prf: { results: { first: sourcePRF.buffer } } })
        }
      }
    }
  }
})

const { getAuthStatus, reauthenticateWithPasskey } = await import('../src/utils/api.js')

afterEach(() => {
  delete globalThis.fetch
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
