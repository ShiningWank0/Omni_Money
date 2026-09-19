import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getAccounts: vi.fn(), getTransactions: vi.fn(), getCreditCardSettings: vi.fn(),
  getBankAccountSettings: vi.fn(), getItems: vi.fn()
}))
vi.mock('../../src/utils/api.js', () => api)
import { useAppStore } from '../../src/store/index.js'

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

beforeEach(() => { setActivePinia(createPinia()) })

const reads = [
  ['fetchAccounts', 'getAccounts', 'accounts', ['cash']],
  ['fetchCreditCardSettings', 'getCreditCardSettings', 'creditCardItems', ['card']],
  ['fetchBankAccountSettings', 'getBankAccountSettings', 'bankAccountItems', ['bank']],
  ['fetchItems', 'getItems', 'itemNames', ['lunch']],
  ['fetchTransactions', 'getTransactions', 'transactions', [{ id: 1, item: 'private' }]]
]

it.each(reads)('%s preserves previously loaded data on failure and clears the error after retry', async (fetch, get, field, data) => {
  const store = useAppStore()
  api[get].mockResolvedValueOnce(data).mockRejectedValueOnce(new Error('unavailable')).mockResolvedValueOnce([])
  await store[fetch]()
  await store[fetch]()
  expect(store[field]).toEqual(data)
  expect(store.loadError).toContain('読み込めませんでした')
  await store[fetch]()
  expect(store[field]).toEqual([])
  expect(store.loadError).toBe('')
})

it.each(reads)('%s cannot repopulate private state after reset', async (fetch, get, field, data) => {
  const store = useAppStore()
  const request = deferred()
  api[get].mockReturnValueOnce(request.promise)
  const pending = store[fetch]()
  store.resetState()
  request.resolve(data)
  await pending
  expect(store[field]).toEqual([])
  expect(store.selectedFundItems).toEqual([])
  expect(store.loadError).toBe('')
  expect(store.loading).toBe(false)
})

it('rejects stale item responses and stale failures after a newer request', async () => {
  const store = useAppStore()
  const older = deferred(), oldest = deferred()
  api.getItems.mockReturnValueOnce(oldest.promise).mockReturnValueOnce(older.promise).mockResolvedValueOnce(['latest'])
  const first = store.fetchItems('one'), second = store.fetchItems('two')
  await store.fetchItems('three')
  older.resolve(['old'])
  oldest.reject(new Error('old failure'))
  await Promise.all([first, second])
  expect(store.itemNames).toEqual(['latest'])
  expect(store.loadError).toBe('')
})
