import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getTags: vi.fn(),
  createTag: vi.fn(),
  createTagByPath: vi.fn(),
  getTransactionLinks: vi.fn(),
  getCardReconciliation: vi.fn(),
  addTransactionLink: vi.fn(),
  removeTransactionLink: vi.fn(),
  getTransactions: vi.fn(),
  getTransactionImages: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))

import TransactionModal from '../../src/components/TransactionModal.vue'

const baseProps = {
  fundItems: ['cash', 'card', 'bank'],
  itemNames: [],
  creditCardItems: ['card'],
  bankAccountItems: ['bank']
}

const linkedTransaction = {
  id: 2,
  date: '2026-01-02',
  account: 'bank',
  fundItem: 'bank',
  item: 'card payment',
  type: 'expense',
  amount: 100
}

beforeEach(() => {
  api.getTags.mockResolvedValue([])
  api.getTransactionLinks.mockResolvedValue([])
  api.getTransactions.mockResolvedValue([])
  api.getTransactionImages.mockResolvedValue([])
  api.getCardReconciliation.mockImplementation(async id => ({
    applicable: true,
    bank: { ...linkedTransaction, id },
    cards: [],
    card_total_exact: '0', bank_amount_exact: '100', difference_exact: '-100',
    matched: false, has_conflicts: false
  }))
})

async function mountModal(props = {}) {
  const wrapper = mount(TransactionModal, { props: { ...baseProps, ...props } })
  await flushPromises()
  return wrapper
}

function fillRequiredFields(wrapper) {
  return Promise.all([
    wrapper.get('input[placeholder="資金項目名を入力または選択"]').setValue('cash'),
    wrapper.get('input[placeholder="例: 給与、食費、交通費"]').setValue('groceries'),
    wrapper.get('input[inputmode="numeric"]').setValue('500')
  ])
}

describe('TransactionModal', () => {
  async function searchLink(wrapper, query) {
    vi.useFakeTimers()
    await wrapper.get('.link-search-input').setValue(query)
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
  }

  it('shows a bank total net of refunds, updates on edits, and saves a mismatch', async () => {
    const cards = [
      { id: 2, fundItem: 'card', date: '2026-01-01', item: 'first', type: 'expense', amount: 300 },
      { id: 3, fundItem: 'card', date: '2026-01-01', item: 'second', type: 'expense', amount: 200 },
      { id: 4, fundItem: 'card', date: '2026-01-01', item: 'refund', type: 'income', amount: 50 }
    ]
    api.getTransactionLinks.mockResolvedValue(cards)
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, account: 'bank', date: '2026-01-02', item: 'withdrawal', type: 'expense', amount: 450 }
    })
    expect(wrapper.get('.card-reconciliation .matched').text()).toBe('一致')
    expect(wrapper.get('.card-reconciliation').text()).toContain('クレカ合計¥450')
    await wrapper.get('input[inputmode="numeric"]').setValue('500')
    expect(wrapper.get('.card-reconciliation .unmatched').text()).toBe('差額あり')
    expect(wrapper.get('.card-reconciliation').text()).toContain('差額¥-50')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].amount).toBe(500)
    expect(wrapper.find('.form-error').exists()).toBe(false)
    await wrapper.findAll('.link-remove')[2].trigger('click')
    expect(wrapper.get('.card-reconciliation .matched').text()).toBe('一致')
  })

  it('includes the other card purchases when previewing an edited card transaction', async () => {
    api.getTransactionLinks.mockResolvedValue([linkedTransaction])
    api.getCardReconciliation.mockResolvedValue({
      applicable: true, bank: { ...linkedTransaction, amount: 300 },
      cards: [
        { id: 1, fundItem: 'card', type: 'expense', amount: 100, linked_bank_ids: [2] },
        { id: 3, fundItem: 'card', type: 'expense', amount: 200, linked_bank_ids: [2] }
      ]
    })
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, date: '2026-01-01', account: 'card', item: 'purchase', type: 'expense', amount: 100 }
    })
    expect(wrapper.get('.card-reconciliation .matched').text()).toBe('一致')
    await wrapper.get('input[inputmode="numeric"]').setValue('150')
    expect(wrapper.get('.card-reconciliation').text()).toContain('クレカ合計¥350')
    expect(wrapper.get('.card-reconciliation').text()).toContain('差額¥50')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].amount).toBe(150)
  })

  it('allows several unassigned cards on a bank and rejects a card owned by another bank', async () => {
    api.getTransactionLinks.mockImplementation(async id => id === 5 ? [linkedTransaction] : [])
    api.getTransactions.mockResolvedValue([
      { id: 3, account: 'card', date: '2026-01-01', item: 'first card', type: 'expense', amount: 200 },
      { id: 4, account: 'card', date: '2026-01-01', item: 'second card', type: 'expense', amount: 300 },
      { id: 5, account: 'card', date: '2026-01-01', item: 'already linked', type: 'expense', amount: 100 }
    ])
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, date: '2026-01-02', account: 'bank', item: 'withdrawal', type: 'expense', amount: 500 }
    })
    await searchLink(wrapper, 'card')
    await wrapper.findAll('.link-search-item')[0].trigger('click')
    await flushPromises()
    await searchLink(wrapper, 'card')
    await wrapper.findAll('.link-search-item')[0].trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.linked-item')).toHaveLength(2)
    expect(wrapper.get('.card-reconciliation .matched').text()).toBe('一致')
    await searchLink(wrapper, 'linked')
    await wrapper.get('.link-search-item').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.linked-item')).toHaveLength(2)
    expect(wrapper.get('.link-conflict').text()).toContain('別の銀行取引に紐付いています')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].link_add_ids).toEqual([3, 4])
  })

  it('preserves legacy multi-bank links until the user explicitly removes one', async () => {
    api.getTransactionLinks.mockResolvedValue([linkedTransaction, { ...linkedTransaction, id: 3 }])
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, date: '2026-01-01', account: 'card', item: 'purchase', type: 'expense', amount: 100 }
    })
    expect(wrapper.findAll('.linked-item')).toHaveLength(2)
    expect(wrapper.find('.link-conflict').exists()).toBe(true)
    expect(wrapper.find('.link-search-input').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].link_remove_ids).toBeUndefined()
    await wrapper.findAll('.link-remove')[1].trigger('click')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[1][0].link_remove_ids).toEqual([3])
  })

  it('does not block saving a transaction when the informational reconciliation request fails', async () => {
    api.getTransactionLinks.mockResolvedValue([linkedTransaction])
    api.getCardReconciliation.mockRejectedValueOnce(new Error('offline'))
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, date: '2026-01-01', account: 'card', item: 'purchase', type: 'expense', amount: 100 }
    })
    expect(wrapper.get('.reconciliation-unavailable').text()).toContain('照合情報を取得できませんでした')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')).toHaveLength(1)
    await wrapper.get('.reconciliation-unavailable button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.reconciliation-unavailable').exists()).toBe(false)
  })

  for (const isEditMode of [false, true]) {
    describe(isEditMode ? 'amount validation when editing' : 'amount validation when creating', () => {
      async function prepareModal() {
        const wrapper = await mountModal(isEditMode ? {
          isEditMode: true,
          transaction: { id: 1, date: '2026-01-01', account: 'cash', item: 'purchase', type: 'expense', amount: 100 }
        } : {})
        await fillRequiredFields(wrapper)
        return wrapper
      }

      it('accepts exactly one billion yen without changing the value', async () => {
        const wrapper = await prepareModal()
        await wrapper.get('input[inputmode="numeric"]').setValue('1000000000')
        await wrapper.get('form').trigger('submit')

        expect(wrapper.emitted('save')).toHaveLength(1)
        expect(wrapper.emitted('save')[0][0].amount).toBe(1000000000)
      })

      it.each([
        ['one yen above the limit', '1000000001'],
        ['beyond safe integer precision', '9007199254740993'],
        ['a 400-digit value', '9'.repeat(400)]
      ])('rejects %s and can save after correction', async (_, value) => {
        const wrapper = await prepareModal()
        const input = wrapper.get('input[inputmode="numeric"]')
        await input.setValue(value)
        await wrapper.get('form').trigger('submit')

        expect(wrapper.emitted('save')).toBeUndefined()
        expect(wrapper.get('.form-error').text()).toBe('金額は1,000,000,000円以下で指定してください')
        expect(input.element.value).toBe(value)

        await input.setValue('500')
        await wrapper.get('form').trigger('submit')
        expect(wrapper.emitted('save')).toHaveLength(1)
        expect(wrapper.emitted('save')[0][0].amount).toBe(500)
        expect(wrapper.find('.form-error').exists()).toBe(false)
      })
    })
  }

  it('keeps a new nested tag local until save', async () => {
    api.getTags.mockResolvedValueOnce([{ id: 4, name: 'Food', children: [] }])
    api.createTag.mockClear()
    api.createTagByPath.mockClear()
    const wrapper = await mountModal()
    await fillRequiredFields(wrapper)
    await wrapper.get('.tag-select').setValue(4)
    await wrapper.get('.new-tag-input').setValue('Lunch')
    await wrapper.get('.new-tag-row .add-tag-btn').trigger('click')

    expect(wrapper.get('.tag-badge').text()).toContain('Food/Lunch')
    expect(api.createTag).not.toHaveBeenCalled()
    expect(api.createTagByPath).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].new_tag_paths).toEqual(['Food/Lunch'])
  })

  it('stages a saved image removal until update and lets the user undo it', async () => {
    api.getTransactionImages.mockResolvedValueOnce([{
      id: 13,
      filename: 'receipt.png',
      data_url: 'data:image/png;base64,AAAA'
    }])
    const wrapper = await mountModal({
      isEditMode: true,
      initialRemoveImageId: 13,
      transaction: { id: 1, date: '2026-01-01', account: 'cash', item: 'purchase', type: 'expense', amount: 100 }
    })

    expect(wrapper.get('.image-preview.pending-removal').text()).toContain('receipt.png')
    await wrapper.get('.image-preview button').trigger('click')
    expect(wrapper.find('.image-preview.pending-removal').exists()).toBe(false)
    await wrapper.get('.image-preview button').trigger('click')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0].delete_image_ids).toEqual([13])
    expect(api.getTransactionImages).toHaveBeenCalledWith(1)
  })

  it('does not emit save while an image is still being read, then includes it after completion', async () => {
    let reader
    vi.spyOn(FileReader.prototype, 'readAsDataURL').mockImplementation(function () {
      reader = this
    })
    const wrapper = await mountModal()
    await fillRequiredFields(wrapper)

    const file = new File(['image'], 'receipt.png', { type: 'image/png' })
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
    await input.trigger('change')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.get('.form-error').text()).toContain('画像の読み込みが完了するまで')

    reader.onload({ target: { result: 'data:image/png;base64,AAAA' } })
    await flushPromises()
    await wrapper.get('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0].images).toEqual([{
      filename: 'receipt.png',
      data: 'AAAA',
      mime_type: 'image/png'
    }])
  })

  it('limits link candidates and stages link changes until the transaction is saved', async () => {
    api.addTransactionLink.mockClear()
    api.removeTransactionLink.mockClear()
    api.getTransactionLinks.mockResolvedValue([linkedTransaction])
    api.getTransactions.mockResolvedValue([
      { id: 1, account: 'card', item: 'current' },
      linkedTransaction,
      { id: 3, date: '2026-01-03', account: 'bank', fundItem: 'bank', item: 'new payment', type: 'expense', amount: 200 },
      { id: 4, date: '2026-01-04', account: 'cash', fundItem: 'cash', item: 'cash entry', type: 'expense', amount: 300 },
      { id: 5, date: '2026-01-05', account: 'card', fundItem: 'card', item: 'card entry', type: 'expense', amount: 400 }
    ])
    const wrapper = await mountModal({
      isEditMode: true,
      transaction: { id: 1, date: '2026-01-01', account: 'card', item: 'purchase', type: 'expense', amount: 100 }
    })

    vi.useFakeTimers()
    expect(wrapper.find('.link-search-input').exists()).toBe(false)
    await wrapper.get('.link-remove').trigger('click')
    const search = wrapper.get('.link-search-input')
    await search.setValue('payment')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    const candidates = wrapper.findAll('.link-search-item')
    expect(candidates).toHaveLength(2)
    const newPayment = candidates.find(candidate => candidate.text().includes('new payment'))

    await newPayment.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.linked-item')).toHaveLength(1)
    expect(wrapper.find('.link-search-input').exists()).toBe(false)
    expect(api.addTransactionLink).not.toHaveBeenCalled()

    await wrapper.get('.link-remove').trigger('click')
    expect(api.removeTransactionLink).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].link_remove_ids).toEqual([linkedTransaction.id])
    expect(wrapper.emitted('save')[0][0].link_add_ids).toBeUndefined()
  })
})
