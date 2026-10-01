import { mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import BalanceChart from '../../src/components/BalanceChart.vue'

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['options'],
    template: '<div class="line-chart" :data-y-min="options.scales.y.min" :data-y-max="options.scales.y.max" />'
  }
}))

afterEach(() => vi.unstubAllGlobals())

it('uses one aspect-ratio rule for narrow and wide graph viewports', async () => {
  let notifyResize
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { notifyResize = callback }
    observe() {}
    disconnect() {}
  })
  const wrapper = mount(BalanceChart, {
    props: {
      balanceHistory: {
        accounts: ['test'],
        dates: ['2026-10-01'],
        balances: { test: [100] }
      }
    }
  })
  const viewport = wrapper.get('.graph-scroll').element
  let height = 600
  let width = 375
  Object.defineProperty(viewport, 'clientWidth', { get: () => width })
  Object.defineProperty(viewport, 'clientHeight', { get: () => height })

  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.get('.graph-container').element.style.width).toBe('1067px')
  expect(wrapper.get('.graph-scroll-hint').classes()).not.toContain('is-hidden')

  height = 1500
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.get('.graph-container').element.style.width).toBe('2667px')

  width = 1400
  height = 600
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.get('.graph-container').element.style.width).toBe('1400px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')
  wrapper.unmount()
})

it('zooms, pans, and resets only the vertical chart range', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const wrapper = mount(BalanceChart, {
    props: {
      balanceHistory: {
        accounts: ['test'],
        dates: ['2026-10-01'],
        balances: { test: [100] }
      }
    }
  })
  const chart = () => wrapper.get('.line-chart')
  expect(chart().attributes('data-y-min')).toBeUndefined()
  await wrapper.get('[aria-label="縦軸を拡大"]').trigger('click')
  expect(chart().attributes('data-y-min')).toBe('96')
  expect(chart().attributes('data-y-max')).toBe('104')
  await wrapper.get('[aria-label="縦軸を上へ移動"]').trigger('click')
  expect(chart().attributes('data-y-min')).toBe('98')
  expect(chart().attributes('data-y-max')).toBe('106')
  await wrapper.get('[aria-label="縦軸を元に戻す"]').trigger('click')
  expect(chart().attributes('data-y-min')).toBeUndefined()

  const graph = wrapper.get('.graph-scroll').element
  Object.defineProperty(graph, 'clientHeight', { value: 600 })
  graph.setPointerCapture = vi.fn()
  graph.hasPointerCapture = vi.fn(() => true)
  graph.releasePointerCapture = vi.fn()
  const pointer = (type, y) => {
    const event = new Event(type, { bubbles: true })
    Object.assign(event, { button: 0, pointerType: 'mouse', pointerId: 1, clientX: 20, clientY: y })
    graph.dispatchEvent(event)
  }
  pointer('pointerdown', 50)
  pointer('pointermove', 110)
  await wrapper.vm.$nextTick()
  expect(chart().attributes('data-y-min')).toBe('96')
  pointer('pointerup', 110)

  await wrapper.get('[aria-label="縦軸を元に戻す"]').trigger('click')
  const wheel = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -100 })
  graph.dispatchEvent(wheel)
  await wrapper.vm.$nextTick()
  expect(wheel.defaultPrevented).toBe(true)
  expect(Number(chart().attributes('data-y-min'))).toBeGreaterThan(95)
  wrapper.unmount()
})
