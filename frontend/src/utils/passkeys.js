function base64urlToBytes(value) {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const binary = window.atob(padded)
  return Uint8Array.from(binary, character => character.charCodeAt(0))
}

function bytesToBase64url(value) {
  const bytes = value instanceof ArrayBuffer
    ? new Uint8Array(value)
    : new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

function requirePasskeySupport() {
  if (!window.isSecureContext || typeof window.PublicKeyCredential === 'undefined' || !navigator.credentials) {
    throw new Error('このブラウザまたは接続ではパスキーを利用できません。HTTPSで対応ブラウザを使用してください')
  }
}

function parseCreationOptions(options) {
  if (typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function') {
    return PublicKeyCredential.parseCreationOptionsFromJSON(options)
  }
  const parsed = structuredClone(options)
  parsed.challenge = base64urlToBytes(parsed.challenge)
  parsed.user.id = base64urlToBytes(parsed.user.id)
  parsed.excludeCredentials = (parsed.excludeCredentials || []).map(item => ({ ...item, id: base64urlToBytes(item.id) }))
  const first = parsed.extensions?.prf?.eval?.first
  if (typeof first === 'string') parsed.extensions.prf.eval.first = base64urlToBytes(first)
  return parsed
}

function parseRequestOptions(options) {
  if (typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function') {
    return PublicKeyCredential.parseRequestOptionsFromJSON(options)
  }
  const parsed = structuredClone(options)
  parsed.challenge = base64urlToBytes(parsed.challenge)
  parsed.allowCredentials = (parsed.allowCredentials || []).map(item => ({ ...item, id: base64urlToBytes(item.id) }))
  const evalByCredential = parsed.extensions?.prf?.evalByCredential || {}
  for (const values of Object.values(evalByCredential)) {
    if (typeof values?.first === 'string') values.first = base64urlToBytes(values.first)
    if (typeof values?.second === 'string') values.second = base64urlToBytes(values.second)
  }
  return parsed
}

function encodeExtensionValue(value) {
  if (value instanceof ArrayBuffer || ArrayBuffer.isView(value)) return bytesToBase64url(value)
  if (Array.isArray(value)) return value.map(encodeExtensionValue)
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, encodeExtensionValue(child)]))
  }
  return value
}

function credentialToJSON(credential) {
  if (typeof credential.toJSON === 'function') return credential.toJSON()
  const response = credential.response
  const encodedResponse = { clientDataJSON: bytesToBase64url(response.clientDataJSON) }
  if ('attestationObject' in response) {
    encodedResponse.attestationObject = bytesToBase64url(response.attestationObject)
    encodedResponse.transports = typeof response.getTransports === 'function' ? response.getTransports() : []
    if (typeof response.getAuthenticatorData === 'function') encodedResponse.authenticatorData = bytesToBase64url(response.getAuthenticatorData())
    if (typeof response.getPublicKey === 'function' && response.getPublicKey()) encodedResponse.publicKey = bytesToBase64url(response.getPublicKey())
    if (typeof response.getPublicKeyAlgorithm === 'function') encodedResponse.publicKeyAlgorithm = response.getPublicKeyAlgorithm()
  } else {
    encodedResponse.authenticatorData = bytesToBase64url(response.authenticatorData)
    encodedResponse.signature = bytesToBase64url(response.signature)
    if (response.userHandle) encodedResponse.userHandle = bytesToBase64url(response.userHandle)
  }
  return {
    id: credential.id,
    rawId: bytesToBase64url(credential.rawId),
    type: credential.type,
    response: encodedResponse,
    clientExtensionResults: encodeExtensionValue(credential.getClientExtensionResults()),
    authenticatorAttachment: credential.authenticatorAttachment || undefined
  }
}

function extractPRFResult(credential) {
  const first = credential.getClientExtensionResults()?.prf?.results?.first
  if (!(first instanceof ArrayBuffer) && !ArrayBuffer.isView(first)) {
    throw new Error('このパスキーはOmni MoneyのVault復号に必要なPRF機能へ対応していません')
  }
  const result = first instanceof ArrayBuffer
    ? new Uint8Array(first.slice(0))
    : new Uint8Array(first.buffer.slice(first.byteOffset, first.byteOffset + first.byteLength))
  if (result.byteLength !== 32) {
    result.fill(0)
    throw new Error('パスキーから安全なVault鍵を取得できませんでした')
  }
  return result
}

export async function createPasskey(options) {
  requirePasskeySupport()
  const credential = await navigator.credentials.create({ publicKey: parseCreationOptions(options.publicKey) })
  if (!credential) throw new Error('パスキー登録がキャンセルされました')
  return { credential: credentialToJSON(credential), prfResult: extractPRFResult(credential) }
}

export async function authenticatePasskey(options) {
  requirePasskeySupport()
  const credential = await navigator.credentials.get({ publicKey: parseRequestOptions(options.publicKey) })
  if (!credential) throw new Error('パスキー認証がキャンセルされました')
  return { credential: credentialToJSON(credential), prfResult: extractPRFResult(credential) }
}

export function passkeysSupported() {
  return window.isSecureContext && typeof window.PublicKeyCredential !== 'undefined' && Boolean(navigator.credentials)
}
