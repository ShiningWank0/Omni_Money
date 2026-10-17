import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

const api = vi.hoisted(() => ({
  acceptServerInvitation: vi.fn(),
  completeServerPasswordReset: vi.fn(),
  getAuthStatus: vi.fn(),
  login: vi.fn(),
  loginWithPasskey: vi.fn(),
  setupInitialAdmin: vi.fn(),
}))
vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))
vi.mock('../../src/utils/passkeys', () => ({ passkeysSupported: () => true }))

import LoginView from '../../src/views/LoginView.vue'
import { recoveryCodeToSecret, recoverySecretToCode } from '../../src/utils/recovery'

const password = 'test-password-for-account-flow'
const oldRecovery = new Uint8Array(32).fill(7)
const oldCode = recoverySecretToCode(oldRecovery)
let media
let layoutListener

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
  api.getAuthStatus.mockResolvedValue({ authenticated: false, setup_required: false })
  api.acceptServerInvitation.mockResolvedValue({ user: { id: 'user', email: 'test@example.invalid' } })
  api.completeServerPasswordReset.mockResolvedValue({ password_reset: { id: 'reset' } })
  api.login.mockRejectedValue(new Error('test login rejected'))
  api.loginWithPasskey.mockRejectedValue(new Error('test passkey rejected'))
  window.history.replaceState({}, '', '/login')
  layoutListener = null
  media = {
    matches: true,
    addEventListener: vi.fn((type, listener) => { layoutListener = listener }),
    removeEventListener: vi.fn(),
  }
  vi.stubGlobal('matchMedia', vi.fn(() => media))
})

async function open(mode = 'invite', compact = true) {
  media.matches = compact
  window.history.replaceState({}, '', '/login?mode=' + mode)
  const wrapper = mount(LoginView, { attachTo: document.body })
  await flushPromises()
  return wrapper
}

async function fillCredentials(wrapper, mode = 'invite') {
  await wrapper.get('#operation-token').setValue('test-operation-token')
  if (mode === 'invite') await wrapper.get('#display-name').setValue('テスト')
  else await wrapper.get('#current-recovery').setValue(oldCode)
  await wrapper.get('#password').setValue(password)
  await wrapper.get('#password-confirmation').setValue(password)
}

async function resize(compact) {
  media.matches = compact
  layoutListener({ matches: compact })
  await nextTick()
}

it('keeps Next/Back and layout changes local without replacing inputs or the saved recovery code', async () => {
  const wrapper = await open()
  await fillCredentials(wrapper)
  const code = wrapper.get('.recovery-code').element.value
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(wrapper.get('.recovery-panel').isVisible()).toBe(false)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(false)
  expect(wrapper.get('.recovery-panel').isVisible()).toBe(true)
  expect(api.acceptServerInvitation).not.toHaveBeenCalled()
  expect(api.completeServerPasswordReset).not.toHaveBeenCalled()
  await wrapper.get('.confirmation-label input').setValue(true)
  await wrapper.get('.back-button').trigger('click')
  expect(wrapper.get('#operation-token').element.value).toBe('test-operation-token')
  expect(wrapper.get('#password').element.value).toBe(password)
  await resize(false)
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(wrapper.get('.recovery-panel').isVisible()).toBe(true)
  await resize(true)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(wrapper.get('.recovery-code').element.value).toBe(code)
  expect(wrapper.get('.confirmation-label input').element.checked).toBe(true)
  expect(api.acceptServerInvitation).not.toHaveBeenCalled()
  wrapper.unmount()
  expect(media.removeEventListener).toHaveBeenCalledWith('change', layoutListener)
})

for (const mode of ['invite', 'reset']) {
  for (const compact of [true, false]) {
    it('commits the same ' + mode + ' payload exactly once in ' + (compact ? 'compact' : 'wide') + ' layout', async () => {
      let captured
      let newBytes
      let currentBytes
      const action = mode === 'invite' ? api.acceptServerInvitation : api.completeServerPasswordReset
      action.mockImplementation(async input => {
        newBytes = input.recoverySecret && mode === 'invite' ? input.recoverySecret : input.newRecoverySecret
        currentBytes = mode === 'reset' ? input.recoverySecret : null
        captured = { ...input }
        if (input.recoverySecret) captured.recoverySecret = input.recoverySecret.slice()
        if (input.newRecoverySecret) captured.newRecoverySecret = input.newRecoverySecret.slice()
        return mode === 'invite' ? { user: { id: 'user', email: 'test@example.invalid' } } : { password_reset: { id: 'reset' } }
      })
      const wrapper = await open(mode, compact)
      await fillCredentials(wrapper, mode)
      const expectedNewSecret = recoveryCodeToSecret(wrapper.get('.recovery-code').element.value)
      if (compact) {
        await wrapper.get('form').trigger('submit')
        await flushPromises()
        expect(action).not.toHaveBeenCalled()
      }
      await wrapper.get('.confirmation-label input').setValue(true)
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(action).toHaveBeenCalledTimes(1)
      expect(captured).toEqual(mode === 'invite'
        ? { token: 'test-operation-token', displayName: 'テスト', password, recoverySecret: expectedNewSecret }
        : { token: 'test-operation-token', recoverySecret: oldRecovery, newPassword: password, newRecoverySecret: expectedNewSecret })
      expect(newBytes.every(byte => byte === 0)).toBe(true)
      if (currentBytes) expect(currentBytes.every(byte => byte === 0)).toBe(true)
      expect(wrapper.find('#operation-token').exists()).toBe(false)
      expect(wrapper.find('.recovery-panel').exists()).toBe(false)
      expect(wrapper.get('#password').element.value).toBe('')
      expect(wrapper.get('.info-message').text()).toContain('ログインしてください')
      expect(api.login).not.toHaveBeenCalled()
      expect(window.location.search).toBe('')
    })
  }
}

it('revalidates hidden credentials and blocks a direct final submit without a saved code', async () => {
  const wrapper = await open('reset')
  await fillCredentials(wrapper, 'reset')
  await wrapper.get('#current-recovery').setValue('invalid-code')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(api.completeServerPasswordReset).not.toHaveBeenCalled()
  await wrapper.get('#current-recovery').setValue(oldCode)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(wrapper.get('.login-button').attributes('disabled')).toBeDefined()
  // Bypass the disabled button to exercise the final handler's own guard.
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.completeServerPasswordReset).not.toHaveBeenCalled()
  await fillCredentials(wrapper, 'reset')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  await wrapper.get('.confirmation-label input').setValue(true)
  await wrapper.get('#password-confirmation').setValue('changed-while-hidden')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.completeServerPasswordReset).not.toHaveBeenCalled()
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(wrapper.get('[role="alert"]').text()).toContain('一致しません')
})

it('requires the operation token and invitation name before advancing', async () => {
  const wrapper = await open()
  await wrapper.get('form').trigger('submit')
  expect(wrapper.get('[role="alert"]').text()).toContain('トークン')
  await fillCredentials(wrapper)
  await wrapper.get('#display-name').setValue('   ')
  await wrapper.get('form').trigger('submit')
  expect(wrapper.get('[role="alert"]').text()).toContain('表示名')
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(api.acceptServerInvitation).not.toHaveBeenCalled()
})

it('blocks duplicate submits and mode changes while the final request is pending', async () => {
  const pending = deferred()
  api.completeServerPasswordReset.mockReturnValueOnce(pending.promise)
  const wrapper = await open('reset', false)
  await fillCredentials(wrapper, 'reset')
  await wrapper.get('.confirmation-label input').setValue(true)
  const code = wrapper.get('.recovery-code').element.value
  await wrapper.get('form').trigger('submit')
  await wrapper.get('form').trigger('submit')
  for (const button of wrapper.findAll('.auth-mode-tabs button')) {
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
  }
  expect(api.completeServerPasswordReset).toHaveBeenCalledTimes(1)
  expect(wrapper.get('.recovery-code').element.value).toBe(code)
  pending.resolve({ password_reset: { id: 'reset' } })
  await flushPromises()
  expect(wrapper.get('.info-message').text()).toContain('再設定が完了')
})

it('returns to editable credentials after an API failure without regenerating the new recovery code', async () => {
  api.acceptServerInvitation.mockRejectedValueOnce(new Error('トークンの有効期限が切れています'))
  const wrapper = await open()
  await fillCredentials(wrapper)
  const code = wrapper.get('.recovery-code').element.value
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  await wrapper.get('.confirmation-label input').setValue(true)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(wrapper.get('.credentials-panel').isVisible()).toBe(true)
  expect(wrapper.get('#password').element.value).toBe('')
  expect(wrapper.get('#operation-token').element.value).toBe('test-operation-token')
  expect(wrapper.get('.recovery-code').element.value).toBe(code)
  expect(wrapper.get('[role="alert"]').text()).toContain('有効期限')
  expect(api.login).not.toHaveBeenCalled()
})

for (const fails of [false, true]) {
  it('does not replace a saved code when a delayed status request ' + (fails ? 'fails' : 'completes'), async () => {
    const pending = deferred()
    api.getAuthStatus.mockReturnValueOnce(pending.promise)
    const wrapper = mount(LoginView, { attachTo: document.body })
    await wrapper.findAll('.auth-mode-tabs button')[1].trigger('click')
    await fillCredentials(wrapper)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const code = wrapper.get('.recovery-code').element.value
    await wrapper.get('.confirmation-label input').setValue(true)
    if (fails) pending.reject(new Error('status unavailable'))
    else pending.resolve({ authenticated: false, setup_required: false })
    await flushPromises()
    expect(wrapper.get('.recovery-code').element.value).toBe(code)
    expect(wrapper.get('.confirmation-label input').element.checked).toBe(true)
    expect(api.acceptServerInvitation).not.toHaveBeenCalled()
  })
}

it('keeps password/passkey login as direct actions and renames only the reset entry', async () => {
  const wrapper = await open('login')
  expect(wrapper.classes()).not.toContain('account-flow')
  expect(wrapper.find('.recovery-panel').exists()).toBe(false)
  expect(wrapper.get('.login-button').text()).toBe('ログイン')
  expect(wrapper.findAll('.auth-mode-tabs button')[2].text()).toBe('パスワード再設定')
  await wrapper.get('#email').setValue('test@example.invalid')
  await wrapper.get('#password').setValue(password)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.login).toHaveBeenCalledWith('test@example.invalid', password)
  await wrapper.get('.passkey-button').trigger('click')
  await flushPromises()
  expect(api.loginWithPasskey).toHaveBeenCalledTimes(1)
})

it('keeps first-admin setup in a single form and handles a subsequent login failure', async () => {
  api.getAuthStatus.mockResolvedValueOnce({ setup_required: true, authenticated: false })
  api.setupInitialAdmin.mockResolvedValueOnce()
  const wrapper = await open('login')
  expect(wrapper.classes()).not.toContain('account-flow')
  expect(wrapper.get('.login-button').text()).toBe('管理者を作成してログイン')
  await wrapper.get('#setup-token').setValue('bootstrap-test-token')
  await wrapper.get('#email').setValue('test@example.invalid')
  await wrapper.get('#display-name').setValue('管理者')
  await wrapper.get('#password').setValue(password)
  await wrapper.get('#password-confirmation').setValue(password)
  await wrapper.get('.confirmation-label input').setValue(true)
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.setupInitialAdmin).toHaveBeenCalledTimes(1)
  expect(api.login).toHaveBeenCalledTimes(1)
  expect(wrapper.find('#setup-token').exists()).toBe(false)
  expect(wrapper.get('.info-message').text()).toContain('管理者アカウントは作成済み')
})
