import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getTags: vi.fn(),
  updateTag: vi.fn(),
  deleteTag: vi.fn(),
  getTagDeleteImpact: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({ ...api }))

import TagManager from '../../src/components/TagManager.vue'

const tags = [{ id: 1, name: 'Travel', level: 1, children: [] }]

beforeEach(() => {
  api.getTagDeleteImpact.mockResolvedValue({ tag_name: 'Travel', descendant_count: 0, transaction_count: 2 })
  api.deleteTag.mockResolvedValue(undefined)
})

describe('TagManager', () => {
  it('renders child tags added to an explicitly reactive parent array', async () => {
    const parentTags = reactive([{ id: 1, name: 'Travel', level: 1, children: [] }])
    const wrapper = mount(TagManager, { props: { tags: parentTags } })
    expect(wrapper.findAll('.tag-manager-row')).toHaveLength(1)

    parentTags[0].children.push({ id: 2, name: 'Train', level: 2, children: [] })
    await wrapper.vm.$nextTick()
    const rows = wrapper.findAll('.tag-manager-row')
    expect(rows).toHaveLength(2)
    expect(rows[1].get('input').element.value).toBe('Train')
    expect(rows[1].get('.tag-level').text()).toBe('L2')
  })

  it('replaces plain tag arrays through setProps after a parent refresh', async () => {
    const wrapper = mount(TagManager, { props: { tags } })
    await wrapper.setProps({ tags: [{ id: 3, name: 'Groceries', level: 1, children: [] }] })
    expect(wrapper.findAll('.tag-manager-row')).toHaveLength(1)
    expect(wrapper.get('.tag-name-input').element.value).toBe('Groceries')
    expect(wrapper.find('[aria-label="Travelの名前"]').exists()).toBe(false)
  })

  it('keeps a row unchanged when deletion is cancelled or rejected', async () => {
    const wrapper = mount(TagManager, { props: { tags } })
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)

    await wrapper.get('.tag-delete-btn').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalled()
    expect(api.deleteTag).not.toHaveBeenCalled()
    expect(wrapper.find('.tag-manager-row').exists()).toBe(true)
    expect(wrapper.emitted('changed')).toBeUndefined()

    confirm.mockReturnValue(true)
    api.deleteTag.mockRejectedValueOnce(new Error('delete failed'))
    await wrapper.get('.tag-delete-btn').trigger('click')
    await flushPromises()

    expect(wrapper.find('.tag-manager-row').exists()).toBe(true)
    expect(wrapper.emitted('changed')).toBeUndefined()
    expect(wrapper.get('[role="alert"]').text()).toContain('delete failed')
  })
})
