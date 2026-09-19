import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ getTagSummary: vi.fn() }))
vi.mock('../../src/utils/api', () => api)
vi.mock('chart.js/auto', () => ({ default: class { destroy() {} } }))
import TagPieChart from '../../src/components/TagPieChart.vue'

it('distinguishes loading, failure and a successful empty result and supports retry', async () => {
  let reject
  api.getTagSummary.mockImplementationOnce(() => new Promise((_, no) => { reject = no })).mockResolvedValueOnce([])
  const wrapper = mount(TagPieChart)
  await flushPromises()
  expect(wrapper.get('[role="status"]').text()).toContain('読み込んでいます')
  expect(wrapper.text()).not.toContain('データがありません')
  reject(new Error('unavailable'))
  await flushPromises()
  expect(wrapper.get('[role="alert"]').text()).toContain('読み込めませんでした')
  expect(wrapper.text()).not.toContain('データがありません')
  await wrapper.get('[role="alert"] button').trigger('click')
  await flushPromises()
  expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('データがありません')
})

it('does not replace the selected period with an older summary', async () => {
  let resolveOld
  api.getTagSummary.mockImplementationOnce(() => new Promise(yes => { resolveOld = yes })).mockResolvedValueOnce([])
  const wrapper = mount(TagPieChart)
  await flushPromises()
  await wrapper.findAll('.tab-btn')[1].trigger('click')
  await flushPromises()
  resolveOld([{ tag_id: 1, tag_name: 'old expense', amount: 10, ratio: 1 }])
  await flushPromises()
  expect(wrapper.text()).not.toContain('old expense')
  expect(wrapper.text()).toContain('データがありません')
})
