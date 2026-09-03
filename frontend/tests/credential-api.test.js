import assert from 'node:assert/strict'
import test from 'node:test'

global.window = {
  location: { origin: 'https://money.example', pathname: '/', replace() {} },
  dispatchEvent() {},
  btoa(value) { return Buffer.from(value, 'binary').toString('base64') }
}
global.CustomEvent = class CustomEvent {
  constructor(type, init) {
    this.type = type
    this.detail = init?.detail
    this.cancelable = init?.cancelable === true
    this.defaultPrevented = false
  }

  preventDefault() {
    if (this.cancelable) this.defaultPrevented = true
  }
}

const calls = []
global.fetch = async (url, options) => {
  calls.push({ url, options })
  return new Response(JSON.stringify({ success: true, reauthentication_required: true }), {
    status: 200, headers: { 'Content-Type': 'application/json' }
  })
}

const { changeServerPassword, rotateServerRecoveryCode } = await import('../src/utils/api.js')

test('password change sends byte credentials and explicit passkey policy', async () => {
  calls.length = 0
  await changeServerPassword({ currentPassword: 'current-password', newPassword: 'replacement-password', revokePasskeys: true })
  assert.equal(calls.length, 1)
  const body = JSON.parse(calls[0].options.body)
  assert.equal(Buffer.from(body.current_password_b64, 'base64').toString(), 'current-password')
  assert.equal(Buffer.from(body.new_password_b64, 'base64').toString(), 'replacement-password')
  assert.equal(body.revoke_passkeys, true)
})

test('recovery rotation sends exactly one client-generated 32-byte secret', async () => {
  calls.length = 0
  const secret = new Uint8Array(32).fill(0x5a)
  await rotateServerRecoveryCode({ currentPassword: 'current-password', newRecoverySecret: secret })
  const body = JSON.parse(calls[0].options.body)
  assert.equal(Buffer.from(body.new_recovery_secret_b64, 'base64').length, 32)
  assert.equal(body.current_password_b64, Buffer.from('current-password').toString('base64'))
})

test('credential HTTP rejection is marked definitive for recovery-code ambiguity handling', async () => {
  global.fetch = async () => new Response(JSON.stringify({ error: 'rejected' }), { status: 400, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(
    rotateServerRecoveryCode({ currentPassword: 'wrong-password', newRecoverySecret: new Uint8Array(32) }),
    error => error.message === 'rejected' && error.definitiveResponse === true
  )
	global.fetch = async () => new Response(JSON.stringify({ error: 'gateway failure' }), { status: 502, headers: { 'Content-Type': 'application/json' } })
	await assert.rejects(
	  rotateServerRecoveryCode({ currentPassword: 'current-password', newRecoverySecret: new Uint8Array(32) }),
	  error => error.message === 'gateway failure' && error.definitiveResponse === false
	)
})

test('login_required credential response requests in-memory state purge before redirect', async () => {
  const events = []
  const redirects = []
  window.dispatchEvent = event => {
    events.push(event)
    event.preventDefault()
    return false
  }
  window.location.replace = value => redirects.push(value)
  global.fetch = async () => new Response(JSON.stringify({ error: 'expired', login_required: true }), {
    status: 401, headers: { 'Content-Type': 'application/json' }
  })

  await assert.rejects(
    changeServerPassword({ currentPassword: 'current-password', newPassword: 'replacement-password', revokePasskeys: false }),
    error => error.loginRequired === true && error.definitiveResponse === true
  )
  assert.equal(events.length, 1)
  assert.equal(events[0].type, 'omni-money:session-expired')
  assert.equal(events[0].detail.reason, 'session-expired')
  assert.equal(redirects.length, 0, 'App handler owns redirect after purging sensitive state')
})
