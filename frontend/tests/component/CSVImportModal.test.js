import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({ importCSV: vi.fn() }))
vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))

import CSVImportModal from '../../src/components/CSVImportModal.vue'

async function selectFile(wrapper, file) {
  const input = wrapper.get('input[type="file"]')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: file ? [file] : []
  })
  await input.trigger('change')
}

async function selectReplace(wrapper) {
  const radio = wrapper.findAll('input[type="radio"]')
    .find(input => input.element.value === 'replace')
  await radio.setValue()
}

describe('CSVImportModal', () => {
  beforeEach(() => {
    api.importCSV.mockResolvedValue(1)
  })

  it('requires replace consent and resets it when the file or mode changes', async () => {
    const wrapper = mount(CSVImportModal)
    const first = new File(['# omni-money-csv-version:3\n'], 'backup-v3.csv', { type: 'text/csv' })
    await selectFile(wrapper, first)
    await selectReplace(wrapper)

    const submit = wrapper.get('.ok-btn')
    const consent = wrapper.get('.replace-confirmation input')
    expect(submit.attributes('disabled')).toBeDefined()
    await submit.trigger('click')
    expect(api.importCSV).not.toHaveBeenCalled()

    await consent.setValue(true)
    expect(submit.attributes('disabled')).toBeUndefined()
    await selectFile(wrapper, new File(['# omni-money-csv-version:3\n'], 'other-v3.csv'))
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(false)
    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.findAll('input[type="radio"]')
      .find(input => input.element.value === 'append').setValue()
    expect(wrapper.find('.replace-warning').exists()).toBe(false)
    expect(submit.attributes('disabled')).toBeUndefined()
    await selectReplace(wrapper)
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(false)
    expect(submit.attributes('disabled')).toBeDefined()
  })

  it('keeps the selected replace input and emits nothing after an API failure', async () => {
    api.importCSV.mockRejectedValueOnce(new Error('invalid CSV'))
    const wrapper = mount(CSVImportModal)
    const file = new File(['# omni-money-csv-version:3\n'], 'backup-v3.csv', { type: 'text/csv' })
    await selectFile(wrapper, file)
    await selectReplace(wrapper)
    await wrapper.get('.replace-confirmation input').setValue(true)
    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()

    expect(api.importCSV).toHaveBeenCalledWith(file, 'replace')
    expect(wrapper.get('.status-error').text()).toContain('invalid CSV')
    expect(wrapper.text()).toContain('backup-v3.csv')
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(true)
    expect(wrapper.get('.ok-btn').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('imported')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('accepts a legacy append file and emits imported only after success', async () => {
    vi.useFakeTimers()
    api.importCSV.mockResolvedValueOnce(2)
    const wrapper = mount(CSVImportModal)
    const legacy = new File(['account,date,item,type,amount\ncash,2026-01-01,lunch,expense,500\n'], 'legacy-v1.csv', { type: 'text/csv' })
    await selectFile(wrapper, legacy)
    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()

    expect(api.importCSV).toHaveBeenCalledWith(legacy, 'append')
    expect(wrapper.get('.status-success').text()).toContain('2件')
    expect(wrapper.emitted('imported')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(1500)
    expect(wrapper.emitted('imported')).toHaveLength(1)
    expect(wrapper.emitted('close')).toBeUndefined()
  })
})
