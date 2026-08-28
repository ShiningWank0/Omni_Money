const recoverySecretBytes = 32

function bytesToBase64(bytes) {
  let binary = ''
  for (const value of bytes) binary += String.fromCharCode(value)
  return window.btoa(binary)
}

export function recoverySecretToCode(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length !== recoverySecretBytes) {
    throw new Error('回復コードの形式が無効です')
  }
  return bytesToBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

export function generateRecoverySecret() {
  return window.crypto.getRandomValues(new Uint8Array(recoverySecretBytes))
}

export function recoveryCodeToSecret(value) {
  const code = value.trim()
  if (!/^[A-Za-z0-9_-]{43}$/.test(code)) {
    throw new Error('回復コードの形式が無効です')
  }
  const standard = code.replace(/-/g, '+').replace(/_/g, '/') + '='
  let binary
  try {
    binary = window.atob(standard)
  } catch {
    throw new Error('回復コードの形式が無効です')
  }
  const bytes = Uint8Array.from(binary, character => character.charCodeAt(0))
  if (bytes.length !== recoverySecretBytes || recoverySecretToCode(bytes) !== code) {
    bytes.fill(0)
    throw new Error('回復コードの形式が無効です')
  }
  return bytes
}

export function destroySecretBytes(bytes) {
  if (bytes instanceof Uint8Array) bytes.fill(0)
}
