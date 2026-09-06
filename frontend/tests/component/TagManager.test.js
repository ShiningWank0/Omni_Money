import { flushPromises, mount } from '@vue/test-utils'
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
