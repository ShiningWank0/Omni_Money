import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  createServerInvitation: vi.fn(),
  createServerPasswordReset: vi.fn(),
  disableServerUser: vi.fn(),
  enableServerUser: vi.fn(),
  listServerInvitations: vi.fn(),
  listServerPasswordResets: vi.fn(),
  listServerUsers: vi.fn(),
  revokeServerInvitation: vi.fn(),
  revokeServerPasswordReset: vi.fn(),
  setServerUserRole: vi.fn(),
  deleteAllPasskeys: vi.fn(),
  deletePasskey: vi.fn(),
  listPasskeys: vi.fn(),
  registerPasskey: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))

const passkeysSupported = vi.hoisted(() => vi.fn())
vi.mock('../../src/utils/passkeys', () => ({ passkeysSupported }))

const navigation = vi.hoisted(() => ({ replaceLocation: vi.fn() }))
vi.mock('../../src/utils/navigation', () => navigation)

import PasskeySettingsModal from '../../src/components/PasskeySettingsModal.vue'
import ServerAccountAdminModal from '../../src/components/ServerAccountAdminModal.vue'

const activeUser = {
  id: 'user-1',
  display_name: 'User One',
  email: 'user-one@example.com',
  role: 'user',
  state: 'active',
  last_login_at: null
}
const disabledUser = {
  id: 'user-2',
  display_name: 'User Two',
  email: 'user-two@example.com',
  role: 'user',
  state: 'disabled',
  last_login_at: null
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function mountAdmin(props = {}) {
  const wrapper = mount(ServerAccountAdminModal, { props })
  await flushPromises()
  return wrapper
}

async function issueInvitation(wrapper) {
  await wrapper.get('.invite-form input[type="email"]').setValue('new-user@example.com')
  await wrapper.get('.invite-form').trigger('submit')
  await flushPromises()
}

async function issuePasswordReset(wrapper) {
  const button = wrapper.findAll('button').find(candidate => candidate.text() === '再設定token')
  expect(button).toBeDefined()
  await button.trigger('click')
  await flushPromises()
}

async function mountPasskey() {
  const wrapper = mount(PasskeySettingsModal)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(window, 'confirm').mockReturnValue(true)

  api.listServerUsers.mockResolvedValue([activeUser])
  api.listServerInvitations.mockResolvedValue([])
  api.listServerPasswordResets.mockResolvedValue([])
  api.createServerInvitation.mockResolvedValue({ token: 'invite-token' })
  api.createServerPasswordReset.mockResolvedValue({ token: 'reset-token' })
  api.setServerUserRole.mockResolvedValue(undefined)
  api.listPasskeys.mockResolvedValue([])
  api.registerPasskey.mockResolvedValue({})
  api.deletePasskey.mockResolvedValue(undefined)
  api.deleteAllPasskeys.mockResolvedValue(undefined)
  passkeysSupported.mockReturnValue(true)
})

describe('ServerAccountAdminModal', () => {
  it('blocks account mutations while a one-time token is visible and confirms close', async () => {
    api.listServerUsers.mockResolvedValue([activeUser, disabledUser])
    api.listServerInvitations.mockResolvedValue([{ id: 'inv-1', email: 'pending@example.com', state: 'pending', expires_at: null }])
    api.listServerPasswordResets.mockResolvedValue([{ id: 'reset-1', user_id: activeUser.id, state: 'pending', expires_at: null }])
    const wrapper = await mountAdmin({ currentUserId: 'self' })

    await issueInvitation(wrapper)

    expect(wrapper.get('.token-panel textarea').element.value).toBe('invite-token')
    expect(wrapper.findAll('.users-section tbody select').every(select => select.element.disabled)).toBe(true)
    expect(wrapper.findAll('.users-section tbody button').every(button => button.element.disabled)).toBe(true)
    expect(wrapper.findAll('.capability-list button').every(button => button.element.disabled)).toBe(true)
    expect(wrapper.get('.invite-form button').element.disabled).toBe(true)

    window.confirm.mockReturnValue(false)
    await wrapper.get('.icon-close').trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.find('.token-panel').exists()).toBe(true)

    window.confirm.mockReturnValue(true)
    await wrapper.get('.icon-close').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.find('.token-panel').exists()).toBe(false)
  })

  it('fails closed when invitation creation returns no token', async () => {
    api.createServerInvitation.mockResolvedValueOnce({})
    const wrapper = await mountAdmin()

    await issueInvitation(wrapper)

    expect(wrapper.find('.token-panel').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('招待tokenを受け取れませんでした')
  })

  it('fails closed when invitation creation fails', async () => {
    api.createServerInvitation.mockRejectedValueOnce(new Error('invitation service unavailable'))
    const wrapper = await mountAdmin()

    await issueInvitation(wrapper)

    expect(wrapper.find('.token-panel').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('invitation service unavailable')
  })

  it('fails closed when password reset returns no token or fails', async () => {
    api.createServerPasswordReset.mockResolvedValueOnce({})
    const missingToken = await mountAdmin()

    await issuePasswordReset(missingToken)

    expect(missingToken.find('.token-panel').exists()).toBe(false)
    expect(missingToken.get('[role="alert"]').text()).toContain('再設定tokenを受け取れませんでした')
    missingToken.unmount()

    api.createServerPasswordReset.mockRejectedValueOnce(new Error('password reset service unavailable'))
    const failedRequest = await mountAdmin()

    await issuePasswordReset(failedRequest)

    expect(failedRequest.find('.token-panel').exists()).toBe(false)
    expect(failedRequest.get('[role="alert"]').text()).toContain('password reset service unavailable')
  })

  it('emits signed-out only after a successful self role change', async () => {
    const wrapper = await mountAdmin({ currentUserId: activeUser.id })
    const role = wrapper.get('.users-section tbody select')

    await role.setValue('admin')
    await flushPromises()

    expect(api.setServerUserRole).toHaveBeenCalledWith(activeUser.id, 'admin')
    expect(wrapper.emitted('signed-out')).toHaveLength(1)
  })

  it('does not emit signed-out when a self role change fails', async () => {
    api.setServerUserRole.mockRejectedValueOnce(new Error('role update failed'))
    const wrapper = await mountAdmin({ currentUserId: activeUser.id })

    await wrapper.get('.users-section tbody select').setValue('admin')
    await flushPromises()

    expect(wrapper.emitted('signed-out')).toBeUndefined()
  })
})

describe('PasskeySettingsModal', () => {
  it('clears the registration password after success', async () => {
    const wrapper = await mountPasskey()
    const password = wrapper.get('input[type="password"]')
    await wrapper.get('input[type="text"]').setValue('MacBook')
    await password.setValue('correct horse battery staple')

    await wrapper.get('.registration-form').trigger('submit')
    await flushPromises()

    expect(api.registerPasskey).toHaveBeenCalledWith({ name: 'MacBook', password: 'correct horse battery staple' })
    expect(password.element.value).toBe('')
  })

  it('clears the registration password after failure', async () => {
    api.registerPasskey.mockRejectedValueOnce(new Error('passkey registration failed'))
    const wrapper = await mountPasskey()
    const password = wrapper.get('input[type="password"]')
    await wrapper.get('input[type="text"]').setValue('MacBook')
    await password.setValue('correct horse battery staple')

    await wrapper.get('.registration-form').trigger('submit')
    await flushPromises()

    expect(password.element.value).toBe('')
    expect(wrapper.get('[role="alert"]').text()).toContain('passkey registration failed')
  })

  it('clears the registration password before the post-registration list refresh completes', async () => {
    const refresh = deferred()
    api.listPasskeys.mockResolvedValueOnce([])
    api.listPasskeys.mockReturnValueOnce(refresh.promise)
    const wrapper = await mountPasskey()
    const password = wrapper.get('input[type="password"]')
    await wrapper.get('input[type="text"]').setValue('MacBook')
    await password.setValue('correct horse battery staple')

    await wrapper.get('.registration-form').trigger('submit')
    await flushPromises()

    expect(api.listPasskeys).toHaveBeenCalledTimes(2)
    expect(password.element.value).toBe('')

    refresh.resolve([])
    await flushPromises()
  })

  it('prevents close while registration is pending', async () => {
    const registration = deferred()
    api.registerPasskey.mockReturnValueOnce(registration.promise)
    const wrapper = await mountPasskey()
    await wrapper.get('input[type="text"]').setValue('MacBook')
    await wrapper.get('input[type="password"]').setValue('correct horse battery staple')

    await wrapper.get('.registration-form').trigger('submit')
    expect(wrapper.get('.icon-close').element.disabled).toBe(true)
    await wrapper.get('.icon-close').trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()

    registration.resolve({})
    await flushPromises()
  })

  it('redirects to login only after individual and bulk deletion succeed', async () => {
    api.listPasskeys.mockResolvedValue([{ id: 'pk-1', name: 'MacBook', created_at: null, last_used_at: null }])
    const deletion = deferred()
    api.deletePasskey.mockReturnValueOnce(deletion.promise)
    const wrapper = await mountPasskey()

    await wrapper.get('.passkey-list .danger').trigger('click')
    expect(navigation.replaceLocation).not.toHaveBeenCalled()
    deletion.resolve()
    await flushPromises()
    expect(navigation.replaceLocation).toHaveBeenCalledWith('/login')

    await wrapper.get('.bulk-revoke .danger').trigger('click')
    await flushPromises()
    expect(navigation.replaceLocation).toHaveBeenCalledTimes(2)
    expect(navigation.replaceLocation).toHaveBeenLastCalledWith('/login')
  })

  it('does not redirect when individual or bulk deletion fails', async () => {
    api.listPasskeys.mockResolvedValue([{ id: 'pk-1', name: 'MacBook', created_at: null, last_used_at: null }])
    api.deletePasskey.mockRejectedValueOnce(new Error('individual deletion failed'))
    api.deleteAllPasskeys.mockRejectedValueOnce(new Error('bulk deletion failed'))
    const wrapper = await mountPasskey()

    await wrapper.get('.passkey-list .danger').trigger('click')
    await flushPromises()
    expect(navigation.replaceLocation).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('individual deletion failed')

    await wrapper.get('.bulk-revoke .danger').trigger('click')
    await flushPromises()
    expect(navigation.replaceLocation).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('bulk deletion failed')
  })
})
