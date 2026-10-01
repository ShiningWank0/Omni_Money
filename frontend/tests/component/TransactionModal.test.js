import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getTags: vi.fn(),
  createTag: vi.fn(),
  createTagByPath: vi.fn(),
  getTransactionLinks: vi.fn(),
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
    const search = wrapper.get('.link-search-input')
    await search.setValue('payment')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    const candidates = wrapper.findAll('.link-search-item')
    expect(candidates).toHaveLength(1)
    expect(candidates[0].text()).toContain('new payment')

    await candidates[0].trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.linked-item')).toHaveLength(2)
    expect(api.addTransactionLink).not.toHaveBeenCalled()

    await wrapper.findAll('.link-remove')[1].trigger('click')
    expect(wrapper.findAll('.linked-item')).toHaveLength(1)
    await wrapper.get('.link-remove').trigger('click')
    expect(api.removeTransactionLink).not.toHaveBeenCalled()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].link_remove_ids).toEqual([linkedTransaction.id])
    expect(wrapper.emitted('save')[0][0].link_add_ids).toBeUndefined()
  })
})
