import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import CreditCardSettingsModal from '../../src/components/CreditCardSettingsModal.vue'

it('waits for save, prevents duplicate submission and preserves the draft after failure', async () => {
  let reject
  const saveItems = vi.fn().mockImplementationOnce(() => new Promise((_, no) => { reject = no })).mockResolvedValueOnce(undefined)
  const wrapper = mount(CreditCardSettingsModal, { props: { fundItems: ['cash', 'card'], selectedItems: ['card'], saveItems } })
  await wrapper.get('.select-button').trigger('click')
  await wrapper.findAll('input[type="checkbox"]')[0].setValue(true)
  await wrapper.get('.ok-btn').trigger('click')
  await wrapper.get('.ok-btn').trigger('click')
  await wrapper.get('.modal-overlay').trigger('click')
  expect(saveItems).toHaveBeenCalledExactlyOnceWith(['card', 'cash'])
  expect(wrapper.emitted('close')).toBeUndefined()
  expect(wrapper.find('.status-message').exists()).toBe(false)
  expect(wrapper.get('.cancel-btn').element.disabled).toBe(true)
  reject(new Error('save failed'))
  await flushPromises()
  expect(wrapper.get('[role="alert"]').text()).toContain('save failed')
  expect(wrapper.findAll('input:checked')).toHaveLength(2)
  await wrapper.get('.ok-btn').trigger('click')
  await flushPromises()
  expect(wrapper.get('[role="status"]').text()).toBe('設定を保存しました')
})

it('preserves the existing selection when clear fails, then clears only after success', async () => {
  const saveItems = vi.fn().mockRejectedValueOnce(new Error('clear failed')).mockResolvedValueOnce(undefined)
  const wrapper = mount(CreditCardSettingsModal, { props: { fundItems: ['card'], selectedItems: ['card'], saveItems } })
  await wrapper.get('.delete-btn').trigger('click')
  await flushPromises()
  expect(wrapper.get('.select-button').text()).toContain('card')
  expect(wrapper.get('[role="alert"]').text()).toContain('clear failed')
  await wrapper.get('.delete-btn').trigger('click')
  await flushPromises()
  expect(saveItems).toHaveBeenLastCalledWith([])
  expect(wrapper.get('.select-button').text()).toContain('選択なし')
})
