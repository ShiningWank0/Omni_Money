import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getTags: vi.fn(), getTransactionLinks: vi.fn(), getTransactions: vi.fn(), getTransactionImages: vi.fn()
}))
vi.mock('../../src/utils/api', () => ({ ...api, isWailsMode: false }))

import TransactionDetailsModal from '../../src/components/TransactionDetailsModal.vue'
import TransactionModal from '../../src/components/TransactionModal.vue'

const pngURL = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII='
const payloads = [
  '<img data-omni-xss src=x onerror="window.__omniSecurityXSS=1">',
  '<svg data-omni-xss onload="window.__omniSecurityXSS=1">',
  '<script data-omni-xss>window.__omniSecurityXSS=1</script>',
  '\" autofocus onfocus="window.__omniSecurityXSS=1" data-omni-xss="attribute'
]

function transaction(text = 'purchase') {
  return {
    id: 1, date: '2026-01-02 12:00:00', account: text, item: text,
    memo: text, type: 'expense', amount: 37, tags: [{ id: 2, name: text }]
  }
}

async function details(tx) {
  const wrapper = mount(TransactionDetailsModal, { props: { transaction: tx } })
  await flushPromises()
  return wrapper
}

async function edit(tx) {
  const wrapper = mount(TransactionModal, {
    props: { isEditMode: true, transaction: tx, fundItems: [tx.account], itemNames: [tx.item] }
  })
  await flushPromises()
  return wrapper
}

function expectNoInjectedDOM(wrapper) {
  // Built-in SVG icons and valid receipt images are expected. Only payload
  // markers or injected executable attributes indicate an escaping failure.
  expect(wrapper.find('[data-omni-xss]').exists()).toBe(false)
  expect(wrapper.find('[onerror*="__omniSecurityXSS"], [onload*="__omniSecurityXSS"], [onfocus*="__omniSecurityXSS"]').exists()).toBe(false)
}

beforeEach(() => {
  api.getTags.mockResolvedValue([])
  api.getTransactionLinks.mockResolvedValue([])
  api.getTransactions.mockResolvedValue([])
  api.getTransactionImages.mockResolvedValue([])
})

describe('persisted transaction text is rendered as data', () => {
  it.each(payloads)('keeps attack text literal in details and edit: %s', async payload => {
    const tx = transaction(payload)
    api.getTags.mockResolvedValue([{ id: 2, name: payload, level: 1, children: [] }])
    const view = await details(tx)
    expect(view.get('.details-item').text()).toBe(payload)
    expect(view.get('.details-fields').text()).toContain(payload)
    expect(view.get('.details-memo').text()).toBe(payload)
    const fields = view.findAll('.details-fields dd')
    expect(fields[1].text()).toBe(payload)
    expect(fields[4].text()).toBe(payload)
    expectNoInjectedDOM(view)

    const editor = await edit(tx)
    const account = editor.get('input[placeholder="資金項目名を入力または選択"]')
    expect(account.element.value).toBe(payload)
    expect(editor.get('input[placeholder="例: 給与、食費、交通費"]').element.value).toBe(payload)
    expect(editor.get('input[placeholder="メモを入力"]').element.value).toBe(payload)
    expect(editor.get('.tag-badge').text()).toContain(payload)
    await account.trigger('focus')
    expect(editor.get('.funditem-dropdown li').text()).toBe(payload)
    expect(editor.get('datalist option').attributes('value')).toBe(payload)
    expectNoInjectedDOM(editor)

    await editor.get('form').trigger('submit')
    expect(editor.emitted('save')[0][0]).toMatchObject({ account: payload, item: payload, memo: payload, tags: [2] })
  })

  it('keeps hostile image metadata in text and attribute values, including the lightbox', async () => {
    const filename = '\"><img data-omni-xss src=x onerror="window.__omniSecurityXSS=1">.png'
    api.getTransactionImages.mockResolvedValue([{ id: 3, filename, data_url: pngURL }])
    const view = await details(transaction())
    expect(view.get('figcaption span').text()).toBe(filename)
    expect(view.get('img').attributes('alt')).toBe(filename)
    expect(view.get('.details-image-open').attributes('aria-label')).toBe(`${filename}を拡大表示`)
    expectNoInjectedDOM(view)
    await view.get('.details-image-open').trigger('click')
    const lightbox = document.querySelector('.details-lightbox')
    expect(lightbox.querySelector('img').getAttribute('alt')).toBe(filename)
    expect(lightbox.querySelector('[data-omni-xss], [onerror]')).toBeNull()
    await view.unmount()

    const editor = await edit(transaction())
    expect(editor.get('img').attributes('alt')).toBe(filename)
    expect(editor.get('.image-preview').text()).toContain(filename)
    expectNoInjectedDOM(editor)
  })
})

describe('saved image URL allowlist', () => {
  const rejected = [
    ['JavaScript', 'javascript:window.__omniSecurityXSS=1'],
    ['HTML data URL', 'data:text/html;base64,PHNjcmlwdD4='],
    ['SVG data URL', 'data:image/svg+xml;base64,PHN2Zz4='],
    ['external HTTPS', 'https://example.invalid/omni-xss.png'],
    ['protocol-relative', '//example.invalid/omni-xss.png'],
    ['blob', 'blob:https://example.invalid/omni-xss'],
    ['attribute breakout', `${pngURL}\" onerror="window.__omniSecurityXSS=1`]
  ]
  it.each(rejected)('does not render %s in details or edit', async (_name, dataURL) => {
    api.getTransactionImages.mockResolvedValue([{ id: 3, filename: 'hostile.png', data_url: dataURL }])
    const view = await details(transaction())
    expect(view.get('.details-invalid-image').text()).toContain('表示できません')
    expect(view.find('img').exists()).toBe(false)
    expect(view.find('.details-image-open').exists()).toBe(false)
    expectNoInjectedDOM(view)
    const editor = await edit(transaction())
    expect(editor.find('img').exists()).toBe(false)
    expectNoInjectedDOM(editor)
  })

  it('does not render a legacy image explicitly marked invalid despite an allowed URL', async () => {
    api.getTransactionImages.mockResolvedValue([{ id: 3, filename: 'invalid.png', data_url: pngURL, invalid: true }])
    expect((await details(transaction())).find('img').exists()).toBe(false)
    expect((await edit(transaction())).find('img').exists()).toBe(false)
  })

  it.each(['jpeg', 'png', 'gif', 'webp'])('continues to render allowed %s URLs in both components', async format => {
    const dataURL = `data:image/${format};base64,AAAA`
    api.getTransactionImages.mockResolvedValue([{ id: 3, filename: `receipt.${format}`, data_url: dataURL }])
    expect((await details(transaction())).get('img').attributes('src')).toBe(dataURL)
    expect((await edit(transaction())).get('img').attributes('src')).toBe(dataURL)
  })
})
