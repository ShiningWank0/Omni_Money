import assert from 'node:assert/strict'
import { afterEach, beforeEach, test } from 'node:test'

class TestPublicKeyCredential {}

globalThis.PublicKeyCredential = TestPublicKeyCredential
globalThis.window = {
  isSecureContext: true,
  PublicKeyCredential: TestPublicKeyCredential,
  atob: globalThis.atob,
  btoa: globalThis.btoa
}

let createOptions
let getOptions
const prfBytes = Uint8Array.from({ length: 32 }, (_, index) => index + 1)
const prfBase64 = () => window.btoa(String.fromCharCode(...prfBytes))
const prfBase64url = () => prfBase64().replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const credential = {
  toJSON: () => ({
    id: 'credential-id',
    type: 'public-key',
    response: {},
    clientExtensionResults: { prf: { enabled: true, results: { first: prfBase64url() } } }
  }),
  getClientExtensionResults: () => ({ prf: { results: { first: prfBytes.buffer } } })
}
let createCredential = credential
let getCredential = credential

Object.defineProperty(globalThis, 'navigator', {
  configurable: true,
  value: {
    credentials: {
      async create(options) {
        createOptions = options
        return createCredential
      },
      async get(options) {
        getOptions = options
        return getCredential
      }
    }
  }
})

const { assertPasskeyPRF, authenticatePasskey, createPasskey, passkeysSupported } = await import('../src/utils/passkeys.js')

beforeEach(() => {
  createOptions = null
  getOptions = null
  createCredential = credential
  getCredential = credential
  window.isSecureContext = true
})

afterEach(() => {
  prfBytes.fill(0)
  for (let index = 0; index < prfBytes.length; index++) prfBytes[index] = index + 1
})

test('passkey registration decodes WebAuthn inputs and copies the PRF secret', async () => {
  const result = await createPasskey({ publicKey: {
    challenge: 'AQID',
    user: { id: 'BAUG', name: 'user@example.test', displayName: 'User' },
    excludeCredentials: [{ id: 'BwgJ', type: 'public-key' }],
    extensions: { prf: { eval: { first: 'CgsM' } } }
  } })

  assert.equal(passkeysSupported(), true)
  assert.deepEqual([...createOptions.publicKey.challenge], [1, 2, 3])
  assert.deepEqual([...createOptions.publicKey.user.id], [4, 5, 6])
  assert.deepEqual([...createOptions.publicKey.extensions.prf.eval.first], [10, 11, 12])
  assert.deepEqual(result.credential, {
    id: 'credential-id',
    type: 'public-key',
    response: {},
    clientExtensionResults: { prf: { enabled: true } }
  })
  assert.equal(JSON.stringify(result.credential).includes(prfBase64()), false)
  assert.equal(JSON.stringify(result.credential).includes(prfBase64url()), false)
  assert.deepEqual([...result.prfResult], [...prfBytes])
  assert.notEqual(result.prfResult.buffer, prfBytes.buffer)
})

test('credential JSON fallback strips the PRF result but keeps other extension outputs', async () => {
  createCredential = {
    id: 'fallback-id',
    rawId: Uint8Array.from([1, 2, 3]).buffer,
    type: 'public-key',
    response: {
      clientDataJSON: Uint8Array.from([4, 5, 6]).buffer,
      attestationObject: Uint8Array.from([7, 8]).buffer
    },
    getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: prfBytes.buffer } } }),
    authenticatorAttachment: 'platform'
  }
  const result = await createPasskey({ publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } })
  assert.deepEqual(result.credential.clientExtensionResults, { prf: { enabled: true } })
  assert.equal(JSON.stringify(result.credential).includes(prfBase64()), false)
  assert.equal(JSON.stringify(result.credential).includes(prfBase64url()), false)
  assert.equal(result.prfResult.byteLength, 32)
})

test('registration classifies PRF support when creation returns no PRF result', async () => {
  createCredential = {
    toJSON: () => ({ id: 'candidate-id', type: 'public-key', response: {} }),
    getClientExtensionResults: () => ({ prf: { enabled: true } })
  }
  const supported = await createPasskey({ publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } })
  assert.equal(supported.prfResult, null)
  assert.equal(supported.prfEnabled, true)
  assert.equal(supported.prfPresent, true)

  createCredential = {
    toJSON: () => ({ id: 'candidate-id', type: 'public-key', response: {} }),
    getClientExtensionResults: () => ({ prf: { enabled: false } })
  }
  const unsupported = await createPasskey({ publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } })
  assert.equal(unsupported.prfResult, null)
  assert.equal(unsupported.prfEnabled, false)
  assert.equal(unsupported.prfPresent, true)

  createCredential = {
    toJSON: () => ({ id: 'candidate-id', type: 'public-key', response: {} }),
    getClientExtensionResults: () => ({})
  }
  const unreported = await createPasskey({ publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } })
  assert.equal(unreported.prfEnabled, false)
  assert.equal(unreported.prfPresent, false)
})

test('registration step-up assertion evaluates the candidate credential', async () => {
  createCredential = {
    toJSON: () => ({ id: 'candidate-id', type: 'public-key', response: {} }),
    getClientExtensionResults: () => ({ prf: { enabled: true } })
  }
  const created = await createPasskey({ publicKey: { challenge: 'AQID', user: { id: 'BAUG' } } })
  assert.equal(created.prfEnabled, true)

  getCredential = {
    toJSON: () => ({
      id: 'candidate-id',
      type: 'public-key',
      response: { signature: 'AA' },
      clientExtensionResults: { prf: { enabled: true, results: { first: prfBase64url() } } }
    }),
    getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: prfBytes.buffer } } })
  }
  const assertion = await assertPasskeyPRF({ publicKey: {
    challenge: 'AQID',
    allowCredentials: [{ id: 'BwgJ', type: 'public-key' }],
    extensions: { prf: { evalByCredential: { BwgJ: { first: 'CgsM' } } } }
  } })
  assert.deepEqual([...getOptions.publicKey.extensions.prf.evalByCredential.BwgJ.first], [10, 11, 12])
  assert.deepEqual(assertion.credential.clientExtensionResults, { prf: { enabled: true } })
  assert.equal(JSON.stringify(assertion.credential).includes(prfBase64()), false)
  assert.equal(JSON.stringify(assertion.credential).includes(prfBase64url()), false)
  assert.equal(assertion.prfResult.byteLength, 32)
  assert.notEqual(assertion.prfResult.buffer, prfBytes.buffer)
})

test('registration step-up assertion fails closed without a PRF output', async () => {
  getCredential = {
    toJSON: () => ({ id: 'candidate-id', type: 'public-key', response: {} }),
    getClientExtensionResults: () => ({ prf: { enabled: true } })
  }
  await assert.rejects(assertPasskeyPRF({ publicKey: { challenge: 'AQID' } }), /PRF出力/)
})

test('passkey authentication decodes per-credential PRF salts', async () => {
  const result = await authenticatePasskey({ publicKey: {
    challenge: 'AQID',
    allowCredentials: [{ id: 'BwgJ', type: 'public-key' }],
    extensions: { prf: { evalByCredential: { BwgJ: { first: 'CgsM' } } } }
  } })

  assert.deepEqual([...getOptions.publicKey.allowCredentials[0].id], [7, 8, 9])
  assert.deepEqual([...getOptions.publicKey.extensions.prf.evalByCredential.BwgJ.first], [10, 11, 12])
  assert.equal(result.prfResult.byteLength, 32)
})

test('discoverable passkey authentication decodes a PRF input without a credential list', async () => {
  const result = await authenticatePasskey({ publicKey: {
    challenge: 'AQID',
    allowCredentials: [],
    extensions: { prf: { eval: { first: 'CgsM' } } }
  } })

  assert.deepEqual(getOptions.publicKey.allowCredentials, [])
  assert.deepEqual([...getOptions.publicKey.extensions.prf.eval.first], [10, 11, 12])
  assert.equal(result.prfResult.byteLength, 32)
})

test('passkeys fail closed outside a secure context', async () => {
  window.isSecureContext = false
  assert.equal(passkeysSupported(), false)
  await assert.rejects(createPasskey({ publicKey: {} }), /HTTPS/)
})
