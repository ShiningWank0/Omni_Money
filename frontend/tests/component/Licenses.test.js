import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

const api = vi.hoisted(() => ({
  getAuthStatus: vi.fn(),
  setupInitialAdmin: vi.fn(), login: vi.fn(), loginWithPasskey: vi.fn(),
  acceptServerInvitation: vi.fn(), completeServerPasswordReset: vi.fn(),
  acknowledgeDesktopVaultRecovery: vi.fn(), migrateLegacyDesktopVault: vi.fn(),
  recoverDesktopVault: vi.fn(), setupDesktopVault: vi.fn(), unlockDesktopVault: vi.fn(),
}))
vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))
vi.mock('../../src/utils/passkeys', () => ({ passkeysSupported: () => false }))
import LoginView from '../../src/views/LoginView.vue'
import DesktopVaultGate from '../../src/components/DesktopVaultGate.vue'
import LicenseEntry from '../../src/components/LicenseEntry.vue'
import LicenseDialog from '../../src/components/LicenseDialog.vue'

beforeEach(() => {
  api.getAuthStatus.mockResolvedValue({ authenticated: false, setup_required: false })
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  window.history.replaceState({}, '', '/login')
})

for (const [name, component, props] of [
  ['logged-out server', LoginView, {}],
  ['desktop setup', DesktopVaultGate, { status: { configured: false } }],
  ['locked desktop', DesktopVaultGate, { status: { configured: true, unlocked: false } }],
  ['public menu', LicenseEntry, { menu: true }],
]) it(`opens the offline licenses for ${name} and closes with the shared X button`, async () => {
  const wrapper = mount(component, { props, attachTo: document.body })
  await flushPromises()
  const trigger = wrapper.findAll('button').find(button => button.text() === 'オープンソースライセンス')
  expect(trigger).toBeDefined()
  expect(trigger.element.closest('.login-card, .desktop-vault-card')).toBeNull()
  await trigger.trigger('click')
  await vi.dynamicImportSettled()
  await flushPromises()
  const dialog = document.querySelector('[role="dialog"]')
  expect(dialog).not.toBeNull()
  expect(dialog.textContent).toContain('ZETETIC')
  expect(dialog.textContent).toContain('Apache-2.0')
  expect(dialog.querySelector('.panel-close-button')).not.toBeNull()
  expect(globalThis.fetch).not.toHaveBeenCalled()
  dialog.querySelector('[aria-label="ライセンスを閉じる"]').click()
  await flushPromises()
  expect(document.querySelector('[role="dialog"]')).toBeNull()
  expect(document.activeElement).toBe(trigger.element)
})

it('closes on Escape and traps keyboard focus in the dialog', async () => {
  const wrapper = mount(LicenseDialog, { attachTo: document.body })
  await nextTick()
  const dialog = document.querySelector('[role="dialog"]')
  dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
  expect(document.activeElement).toBe(dialog.querySelector('.panel-close-button'))
  dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  expect(wrapper.emitted('close')).toHaveLength(1)
})
