import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  addTransaction: vi.fn(),
  updateTransaction: vi.fn(),
  deleteTransaction: vi.fn(),
  backupToCSVFile: vi.fn(),
  saveCreditCardSettings: vi.fn(),
  saveBankAccountSettings: vi.fn(),
  getBalanceHistoryFiltered: vi.fn(),
  logout: vi.fn(),
  getAuthStatus: vi.fn(),
  getDesktopVaultStatus: vi.fn(),
  lockDesktopVault: vi.fn(),
  enableDesktopFinancialCalls: vi.fn(),
  invalidateDesktopFinancialCalls: vi.fn(),
  reauthenticate: vi.fn(),
  reauthenticateWithPasskey: vi.fn(),
  keepAlive: vi.fn(),
  getTags: vi.fn(),
  getAccounts: vi.fn(),
  getTransactions: vi.fn(),
  getCreditCardSettings: vi.fn(),
  getBankAccountSettings: vi.fn(),
  getItems: vi.fn(),
  createTag: vi.fn(),
  createTagByPath: vi.fn(),
  getTransactionLinks: vi.fn(),
  addTransactionLink: vi.fn(),
  removeTransactionLink: vi.fn(),
  acknowledgeDesktopVaultRecovery: vi.fn(),
  migrateLegacyDesktopVault: vi.fn(),
  recoverDesktopVault: vi.fn(),
  setupDesktopVault: vi.fn(),
  unlockDesktopVault: vi.fn(),
  listSnapshots: vi.fn(),
  restoreSnapshot: vi.fn(),
  clearSessionSecrets: vi.fn(),
  importCSV: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))

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
  api.getAuthStatus.mockResolvedValue({
    authenticated: true,
    idle_timeout_seconds: 0,
    features: { admin: false, ai: false, snapshots: true, passkeys: false },
    user: { id: 'test-user' }
  })
  api.getAccounts.mockResolvedValue(['cash'])
  api.getCreditCardSettings.mockResolvedValue([])
  api.getBankAccountSettings.mockResolvedValue([])
  api.getItems.mockResolvedValue([])
  api.listSnapshots.mockResolvedValue(['omni_money_20260102_030405.db'])
  api.importCSV.mockResolvedValue(1)
})

it('routes a server restore through App session expiry and rejects late private data', async () => {
  const lateTransactions = deferred()
  const restore = deferred()
  api.getTransactions.mockReturnValueOnce(lateTransactions.promise)
  api.restoreSnapshot.mockReturnValueOnce(restore.promise)

  const pinia = createPinia()
  const wrapper = mount(App, { global: { plugins: [pinia] } })
  const store = useAppStore(pinia)
  await flushPromises()
  expect(store.accounts).toEqual(['cash'])

  await wrapper.get('.hamburger-menu').trigger('click')
  await clickButton(wrapper, 'スナップショット管理')
  await vi.dynamicImportSettled()
  await flushPromises()
  expect(wrapper.find('.snapshot-modal').exists()).toBe(true)

  let expiredEvent
  window.addEventListener('omni-money:session-expired', event => { expiredEvent = event })
  await wrapper.get('.restore-btn').trigger('click')
  await wrapper.get('.confirm-yes-btn').trigger('click')
  restore.resolve()
  await flushPromises()

  expect(expiredEvent?.detail.reason).toBe('snapshot-restored')
  expect(expiredEvent?.defaultPrevented).toBe(true)
  expect(api.clearSessionSecrets).toHaveBeenCalledOnce()
  expect(navigation.replaceLocation).toHaveBeenCalledWith('/login?reason=snapshot-restored')
  expect(navigation.reloadLocation).not.toHaveBeenCalled()
  expect(store.accounts).toEqual([])
  expect(store.transactions).toEqual([])
  expect(wrapper.find('.snapshot-modal').exists()).toBe(false)
  expect(wrapper.find('.idle-lock-curtain').exists()).toBe(true)

  lateTransactions.resolve([{ id: 99, account: 'cash', item: 'must stay hidden' }])
  await flushPromises()
  expect(store.transactions).toEqual([])
})

async function selectCSVFile(wrapper, file) {
  const input = wrapper.get('input[type="file"]')
  Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
  await input.trigger('change')
}

it('unmounts the CSV modal and refreshes Pinia only after a successful imported emit', async () => {
  api.getTransactions.mockResolvedValue([])
  const pinia = createPinia()
  const wrapper = mount(App, { global: { plugins: [pinia] } })
  const store = useAppStore(pinia)
  await flushPromises()

  await wrapper.get('.hamburger-menu').trigger('click')
  await clickButton(wrapper, 'CSVインポート')
  await vi.dynamicImportSettled()
  await flushPromises()
  expect(wrapper.find('.csv-import-modal').exists()).toBe(true)

  const legacy = new File([
    'account,date,item,type,amount\ncash,2026-01-01,lunch,expense,500\n'
  ], 'legacy-v1.csv', { type: 'text/csv' })
  await selectCSVFile(wrapper, legacy)
  api.getAccounts.mockResolvedValue(['after-import'])
  vi.useFakeTimers()
  await wrapper.get('.csv-import-modal .ok-btn').trigger('click')
  await flushPromises()

  expect(api.importCSV).toHaveBeenCalledWith(legacy, 'append')
  expect(wrapper.find('.csv-import-modal').exists()).toBe(true)
  await vi.advanceTimersByTimeAsync(1500)
  await flushPromises()

  expect(wrapper.find('.csv-import-modal').exists()).toBe(false)
  expect(store.accounts).toEqual(['after-import'])
})
