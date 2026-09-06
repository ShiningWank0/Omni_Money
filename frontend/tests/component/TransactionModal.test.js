import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getTags: vi.fn(),
  createTag: vi.fn(),
  createTagByPath: vi.fn(),
  getTransactionLinks: vi.fn(),
  addTransactionLink: vi.fn(),
  removeTransactionLink: vi.fn(),
  getTransactions: vi.fn()
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

  it('limits link candidates to counterpart accounts and keeps link state on API failures', async () => {
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
    const search = wrapper.get('.link-search-input')
    await search.setValue('payment')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    const candidates = wrapper.findAll('.link-search-item')
    expect(candidates).toHaveLength(1)
    expect(candidates[0].text()).toContain('new payment')

    api.addTransactionLink.mockRejectedValueOnce(new Error('link failed'))
    await candidates[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('.form-error').text()).toContain('link failed')
    expect(wrapper.find('.link-search-results').exists()).toBe(true)

    api.removeTransactionLink.mockRejectedValueOnce(new Error('unlink failed'))
    await wrapper.get('.link-remove').trigger('click')
    await flushPromises()
    expect(wrapper.get('.form-error').text()).toContain('unlink failed')
    expect(wrapper.find('.linked-item').exists()).toBe(true)
  })
})
