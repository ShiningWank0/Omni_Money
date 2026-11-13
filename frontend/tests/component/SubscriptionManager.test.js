import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getSubscriptions: vi.fn(), saveSubscription: vi.fn(), getSubscriptionCalendar: vi.fn(), getSubscriptionCandidates: vi.fn(),
  dismissSubscriptionCandidate: vi.fn(), getSubscriptionTransactions: vi.fn(), getTags: vi.fn()
}))
vi.mock('../../src/utils/api', () => api)
import SubscriptionManager from '../../src/components/SubscriptionManager.vue'

const source = { id: 1, account: 'card', item: 'Music', type: 'expense', date: '2026-01-31', amount: 1000, amount_exact: '1000', memo: '', tags: [{ id: 3 }] }
const rule = { id: '6425a5b7-bf0d-4a2f-bb61-5ef6dd4196a3', source_transaction_id: 1, revision: 2, account: 'card', item: 'Music', memo: '', interval: 'monthly', currency: 'USD', original_amount: '9.99', anchor_day: 31, anchor_month: 1, timezone: 'Asia/Tokyo', next_date: '2026-11-30', enabled: true, tags: [3] }
const payment = { ...source, id: 2, subscription: { rule_id: rule.id, unconfirmed: true, currency: 'USD', original_amount: '9.99', rate: '150', rate_date: '2026-01-30' } }
async function click(wrapper, text) {
  const button = wrapper.findAll('button').find(button => button.text() === text)
  expect(button, text).toBeDefined(); await button.trigger('click'); await flushPromises()
}
beforeEach(() => {
  api.getSubscriptions.mockResolvedValue([{ ...rule }])
  api.getTags.mockResolvedValue([{ id: 3, name: 'Fun', children: [] }])
  api.getSubscriptionCalendar.mockResolvedValue([])
  api.getSubscriptionCandidates.mockResolvedValue([])
  api.getSubscriptionTransactions.mockResolvedValue({ transactions: [payment], next_offset: -1 })
  api.saveSubscription.mockResolvedValue({ ...rule })
})

it('requires explicit saving and clears the yen amount when switching to a foreign currency', async () => {
  const wrapper = mount(SubscriptionManager, { props: { sourceTransaction: source } })
  await flushPromises()
  expect(api.saveSubscription).not.toHaveBeenCalled()
  const selects = wrapper.findAll('select')
  await selects[1].setValue('USD')
  const amount = wrapper.get('input[inputmode="decimal"]')
  expect(amount.element.value).toBe('')
  await amount.setValue('9.99')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(api.saveSubscription).toHaveBeenCalledWith('', expect.objectContaining({ source_transaction_id: 1, original_amount: '9.99', currency: 'USD', anchor_day: 31, tags: [3] }))
  expect(wrapper.emitted('changed')).toHaveLength(1)
})

it('keeps the draft and does not signal success when saving fails', async () => {
  api.saveSubscription.mockRejectedValueOnce(new Error('not saved'))
  const wrapper = mount(SubscriptionManager, { props: { sourceTransaction: source } })
  await flushPromises()
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(wrapper.find('form').exists()).toBe(true)
  expect(wrapper.get('[role="alert"]').text()).toContain('not saved')
  expect(wrapper.emitted('changed')).toBeUndefined()
})

it('stops only the selected rule and requires reviewing a next date to resume', async () => {
  const wrapper = mount(SubscriptionManager); await flushPromises()
  await click(wrapper, '停止')
  expect(api.saveSubscription).toHaveBeenCalledWith(rule.id, expect.objectContaining({ enabled: false, revision: 2, next_date: rule.next_date }))
  api.getSubscriptions.mockResolvedValue([{ ...rule, enabled: false }])
  await click(wrapper, '再読み込み'); api.saveSubscription.mockClear()
  await click(wrapper, '再開')
  expect(wrapper.find('form').exists()).toBe(true)
  expect(api.saveSubscription).not.toHaveBeenCalled()
  expect(wrapper.get('input[type="checkbox"]').element.checked).toBe(true)
})

it('opens far future schedules without creating a transaction or saving a rule', async () => {
  api.getSubscriptionCalendar.mockResolvedValue([{ date: '2034-02-28', rule_id: rule.id, item: 'Music', account: 'card', currency: 'USD', original_amount: '9.99' }])
  const wrapper = mount(SubscriptionManager); await flushPromises()
  await click(wrapper, 'カレンダー')
  await wrapper.get('input[type="month"]').setValue('2034-02'); await flushPromises()
  expect(api.getSubscriptionCalendar).toHaveBeenLastCalledWith('2034-02')
  await wrapper.get('.subscription-calendar-payment').trigger('click')
  expect(wrapper.find('form').exists()).toBe(true)
  expect(api.saveSubscription).not.toHaveBeenCalled()
  expect(wrapper.emitted('open-transaction')).toBeUndefined()
})

it('uses only opacity for generated calendar entries and opens the original transaction', async () => {
  api.getSubscriptionCalendar.mockResolvedValue([{ date: '2026-11-30', rule_id: rule.id, item: 'Music', account: 'card', currency: 'USD', original_amount: '9.99', transaction: payment }])
  const wrapper = mount(SubscriptionManager); await flushPromises()
  await click(wrapper, 'カレンダー')
  await wrapper.get('input[type="month"]').setValue('2026-11'); await flushPromises()
  const button = wrapper.get('.subscription-calendar-payment')
  expect(button.classes()).toContain('subscription-faded')
  expect(button.attributes('title')).toBeUndefined()
  expect(wrapper.text()).not.toMatch(/未確認|確認待ち|自動登録済み/)
  await button.trigger('click')
  expect(wrapper.emitted('open-transaction')[0][0].id).toBe(payment.id)
})

it('combines the stable rule ID with history filters and keeps faded entries editable', async () => {
  const wrapper = mount(SubscriptionManager); await flushPromises()
  await click(wrapper, '取引を表示')
  const inputs = wrapper.findAll('.subscription-history-filters input')
  await inputs[0].setValue('card'); await inputs[1].setValue('edited')
  await inputs[2].setValue('2026-01-01'); await inputs[3].setValue('2026-12-31')
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.getSubscriptionTransactions).toHaveBeenLastCalledWith(rule.id, { account: 'card', search: 'edited', start: '2026-01-01', end: '2026-12-31', offset: 0 })
  expect(wrapper.get('.subscription-history-payment').classes()).toContain('subscription-faded')
  await wrapper.get('.subscription-history-payment').trigger('click')
  expect(wrapper.emitted('open-transaction')[0][0].id).toBe(payment.id)
})

it('never accepts suggestions implicitly and persists explicit rejections', async () => {
  api.getSubscriptionCandidates.mockResolvedValue([{ fingerprint: 'a'.repeat(64), interval: 'monthly', transaction: source, count: 3 }])
  const wrapper = mount(SubscriptionManager); await flushPromises()
  await click(wrapper, '候補')
  expect(api.saveSubscription).not.toHaveBeenCalled()
  await click(wrapper, '今後表示しない')
  expect(api.dismissSubscriptionCandidate).toHaveBeenCalledWith('a'.repeat(64))
  expect(wrapper.text()).toContain('現在の候補はありません')
})
