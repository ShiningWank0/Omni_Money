import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({ wails: false }))
const api = vi.hoisted(() => ({
  importCSV: vi.fn(),
  previewCSVImport: vi.fn(),
  isWailsMode: false
}))
vi.mock('../../src/utils/api', () => ({
  ...api,
  get isWailsMode() {
    return state.wails
  }
}))

import CSVImportModal from '../../src/components/CSVImportModal.vue'

const previewResult = () => ({
  mode: 'append',
  source_digest: 'a'.repeat(64),
  target_digest: 'b'.repeat(64),
  new_count: 1,
  duplicate_count: 1,
  conflict_count: 1,
  replace_impact: null
})

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

async function runPreview(wrapper) {
  await wrapper.get('.preview-btn').trigger('click')
  await flushPromises()
}

const pins = () => ({ sourceDigest: 'a'.repeat(64), targetDigest: 'b'.repeat(64) })

describe('CSVImportModal', () => {
  beforeEach(() => {
    state.wails = false
    api.isWailsMode = false
    api.importCSV.mockResolvedValue(1)
    api.previewCSVImport.mockImplementation(async (_content, mode) => ({
      ...previewResult(),
      mode,
      replace_impact: mode === 'replace'
        ? { transactions: 2, images: 1, tags: 3, transaction_tags: 1, transaction_links: 1, ledger_settings: 2 }
        : null
    }))
  })

  afterEach(() => {
    state.wails = false
  })

  it('renders the preview classification and replace impact, then applies with pins', async () => {
    const wrapper = mount(CSVImportModal)
    const file = new File(['# omni-money-csv-version:3\n'], 'backup-v3.csv', { type: 'text/csv' })
    await selectFile(wrapper, file)
    await selectReplace(wrapper)
    await runPreview(wrapper)

    expect(api.previewCSVImport).toHaveBeenCalledWith(file, 'replace')
    const preview = wrapper.get('.import-preview')
    expect(preview.text()).toContain('重複候補')
    expect(wrapper.get('.preview-impact').text()).toContain('置換により削除されます')
    expect(wrapper.get('.preview-impact').text()).toContain('取引 2件')
    expect(wrapper.get('.preview-btn').text()).toContain('再プレビュー')

    await wrapper.get('.replace-confirmation input').setValue(true)
    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()
    expect(api.importCSV).toHaveBeenCalledWith(file, 'replace', pins())
    expect(wrapper.emitted('imported')).toBeUndefined()
  })

  it('requires a fresh preview before apply and resets it when the file or mode changes', async () => {
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
    // consent alone is not enough: apply stays gated on a fresh preview
    expect(submit.attributes('disabled')).toBeDefined()
    await runPreview(wrapper)
    expect(submit.attributes('disabled')).toBeDefined()
    await consent.setValue(true)
    expect(submit.attributes('disabled')).toBeUndefined()

    await selectFile(wrapper, new File(['# omni-money-csv-version:3\n'], 'other-v3.csv'))
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(false)
    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.findAll('input[type="radio"]')
      .find(input => input.element.value === 'append').setValue()
    expect(wrapper.find('.replace-warning').exists()).toBe(false)
    expect(submit.attributes('disabled')).toBeDefined()
    await runPreview(wrapper)
    expect(submit.attributes('disabled')).toBeUndefined()
    await selectReplace(wrapper)
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(false)
    expect(submit.attributes('disabled')).toBeDefined()
  })

  it('keeps the selected replace input, resets the preview and emits nothing after an apply failure', async () => {
    api.importCSV.mockRejectedValueOnce(new Error('invalid CSV'))
    const wrapper = mount(CSVImportModal)
    const file = new File(['# omni-money-csv-version:3\n'], 'backup-v3.csv', { type: 'text/csv' })
    await selectFile(wrapper, file)
    await selectReplace(wrapper)
    await runPreview(wrapper)
    await wrapper.get('.replace-confirmation input').setValue(true)
    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()

    expect(api.importCSV).toHaveBeenCalledWith(file, 'replace', pins())
    expect(wrapper.get('.status-error').text()).toContain('invalid CSV')
    expect(wrapper.text()).toContain('backup-v3.csv')
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(true)
    // A failed apply invalidates the consumed preview; re-preview before retry.
    expect(submitDisabled(wrapper)).toBe(true)
    expect(wrapper.emitted('imported')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('accepts a legacy append file and emits imported only after success', async () => {
    vi.useFakeTimers()
    api.importCSV.mockResolvedValueOnce(2)
    const wrapper = mount(CSVImportModal)
    const legacy = new File(['account,date,item,type,amount\ncash,2026-01-01,lunch,expense,500\n'], 'legacy-v1.csv', { type: 'text/csv' })
    await selectFile(wrapper, legacy)
    await runPreview(wrapper)
    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()

    expect(api.importCSV).toHaveBeenCalledWith(legacy, 'append', pins())
    expect(wrapper.get('.status-success').text()).toContain('2件')
    expect(wrapper.emitted('imported')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()

    await vi.advanceTimersByTimeAsync(1500)
    expect(wrapper.emitted('imported')).toHaveLength(1)
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('surfaces preview failures without enabling apply', async () => {
    api.previewCSVImport.mockRejectedValueOnce(new Error('プレビュー失敗'))
    const wrapper = mount(CSVImportModal)
    await selectFile(wrapper, new File(['account,date,item,type,amount\n'], 'broken.csv', { type: 'text/csv' }))
    await runPreview(wrapper)

    expect(wrapper.get('.status-error').text()).toContain('プレビュー失敗')
    expect(submitDisabled(wrapper)).toBe(true)
    expect(api.importCSV).not.toHaveBeenCalled()
    expect(wrapper.emitted('imported')).toBeUndefined()
  })

  it.each(['file', 'mode'])('discards an in-flight preview after a %s change', async (change) => {
    let resolvePreview
    api.previewCSVImport.mockImplementationOnce(() => new Promise(resolve => { resolvePreview = resolve }))
    const wrapper = mount(CSVImportModal)
    await selectFile(wrapper, new File(['first'], 'first.csv'))
    await wrapper.get('.preview-btn').trigger('click')
    if (change === 'file') await selectFile(wrapper, new File(['second'], 'second.csv'))
    else await selectReplace(wrapper)
    resolvePreview(previewResult())
    await flushPromises()
    expect(wrapper.find('.import-preview').exists()).toBe(false)
    expect(submitDisabled(wrapper)).toBe(true)
    expect(api.importCSV).not.toHaveBeenCalled()
  })

  it('disables apply and resets consent while refreshing a preview', async () => {
    const wrapper = mount(CSVImportModal)
    await selectFile(wrapper, new File(['csv'], 'backup.csv'))
    await selectReplace(wrapper)
    await runPreview(wrapper)
    await wrapper.get('.replace-confirmation input').setValue(true)
    let resolvePreview
    api.previewCSVImport.mockImplementationOnce(() => new Promise(resolve => { resolvePreview = resolve }))
    await wrapper.get('.preview-btn').trigger('click')
    expect(submitDisabled(wrapper)).toBe(true)
    expect(wrapper.get('.replace-confirmation input').element.checked).toBe(false)
    await wrapper.get('.ok-btn').trigger('click')
    expect(api.importCSV).not.toHaveBeenCalled()
    resolvePreview({ ...previewResult(), mode: 'replace', replace_impact: {} })
    await flushPromises()
    expect(submitDisabled(wrapper)).toBe(true)
  })

  it('keeps the desktop single-step flow without a preview gate', async () => {
    vi.useFakeTimers()
    state.wails = true
    api.importCSV.mockResolvedValue(3)
    const wrapper = mount(CSVImportModal)
    expect(wrapper.find('.preview-btn').exists()).toBe(false)
    // The desktop file input is replaced by the native picker notice.
    expect(wrapper.find('input[type="file"]').exists()).toBe(false)

    await wrapper.get('.ok-btn').trigger('click')
    await flushPromises()

    expect(api.importCSV).toHaveBeenCalledWith(null, 'append', null)
    expect(wrapper.emitted('imported')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1500)
    expect(wrapper.emitted('imported')).toHaveLength(1)
  })
})

function submitDisabled(wrapper) {
  return wrapper.get('.ok-btn').attributes('disabled') !== undefined
}
