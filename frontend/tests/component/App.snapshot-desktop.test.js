import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  addTransaction: vi.fn(), updateTransaction: vi.fn(), deleteTransaction: vi.fn(),
  backupToCSVFile: vi.fn(), saveCreditCardSettings: vi.fn(), saveBankAccountSettings: vi.fn(),
  getBalanceHistoryFiltered: vi.fn(), logout: vi.fn(), getAuthStatus: vi.fn(),
  getDesktopVaultStatus: vi.fn(), lockDesktopVault: vi.fn(),
  enableDesktopFinancialCalls: vi.fn(), invalidateDesktopFinancialCalls: vi.fn(),
  reauthenticate: vi.fn(), reauthenticateWithPasskey: vi.fn(), keepAlive: vi.fn(),
  getTags: vi.fn(), getAccounts: vi.fn(), getTransactions: vi.fn(),
  getCreditCardSettings: vi.fn(), getBankAccountSettings: vi.fn(), getItems: vi.fn(),
  createTag: vi.fn(), createTagByPath: vi.fn(), getTransactionLinks: vi.fn(),
  addTransactionLink: vi.fn(), removeTransactionLink: vi.fn(),
  acknowledgeDesktopVaultRecovery: vi.fn(), migrateLegacyDesktopVault: vi.fn(),
  recoverDesktopVault: vi.fn(), setupDesktopVault: vi.fn(), unlockDesktopVault: vi.fn(),
  listSnapshots: vi.fn(), restoreSnapshot: vi.fn(), clearSessionSecrets: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: true }))

const navigation = vi.hoisted(() => ({ replaceLocation: vi.fn(), reloadLocation: vi.fn() }))
vi.mock('../../src/utils/navigation', () => navigation)

import App from '../../src/App.vue'
import { useAppStore } from '../../src/store/index.js'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function clickButton(wrapper, label) {
  const button = wrapper.findAll('button').find(candidate => candidate.text().includes(label))
  expect(button, `button containing ${label}`).toBeDefined()
  await button.trigger('click')
}

beforeEach(() => {
  const unlocked = { state: 'unlocked', configured: true, unlocked: true }
  api.getDesktopVaultStatus.mockResolvedValue(unlocked)
  api.getAccounts.mockResolvedValue(['cash'])
  api.getTransactions.mockResolvedValue([])
  api.getCreditCardSettings.mockResolvedValue([])
  api.getBankAccountSettings.mockResolvedValue([])
  api.getItems.mockResolvedValue([])
  api.listSnapshots.mockResolvedValue(['omni_money_20260102_030405.db'])
  api.restoreSnapshot.mockResolvedValue()
})

it('routes the restored emit through the generation gate and locks before completion', async () => {
  const lock = deferred()
  api.lockDesktopVault.mockReturnValueOnce(lock.promise)
  const pinia = createPinia()
  const wrapper = mount(App, { global: { plugins: [pinia] } })
  const store = useAppStore(pinia)
  await flushPromises()
  expect(wrapper.find('.desktop-vault-gate').exists()).toBe(false)
  expect(store.accounts).toEqual(['cash'])

  await wrapper.get('.hamburger-menu').trigger('click')
  await clickButton(wrapper, 'スナップショット管理')
  await vi.dynamicImportSettled()
  await flushPromises()
  await wrapper.get('.restore-btn').trigger('click')
  await wrapper.get('.confirm-yes-btn').trigger('click')
  await flushPromises()

  expect(api.invalidateDesktopFinancialCalls).toHaveBeenCalledOnce()
  expect(api.lockDesktopVault).toHaveBeenCalledWith('manual')
  expect(api.invalidateDesktopFinancialCalls.mock.invocationCallOrder[0])
    .toBeLessThan(api.lockDesktopVault.mock.invocationCallOrder[0])
  expect(navigation.reloadLocation).not.toHaveBeenCalled()
  expect(store.accounts).toEqual([])
  expect(wrapper.find('.snapshot-modal').exists()).toBe(false)
  expect(wrapper.find('.idle-lock-curtain').exists()).toBe(true)

  lock.resolve({ state: 'locked', configured: true, unlocked: false })
  await flushPromises()
  expect(navigation.reloadLocation).toHaveBeenCalledOnce()
  expect(navigation.replaceLocation).not.toHaveBeenCalled()
  expect(wrapper.find('.desktop-vault-gate').exists()).toBe(true)
})
