import { mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import BalanceChart from '../../src/components/BalanceChart.vue'

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['options'],
    template: `<div class="line-chart"
      :data-y-min="options.scales.y.min"
      :data-y-max="options.scales.y.max"
      :data-tick-precision="options.scales.y.ticks.precision"
      :data-tick-label="options.scales.y.ticks.callback(-2005.935)" />`
  }
}))

afterEach(() => vi.unstubAllGlobals())

const DAY = 24 * 60 * 60 * 1000

function historyFor(dates) {
  return {
    accounts: ['test'],
    dates,
    balances: { test: dates.map((_, index) => 100 + index) }
  }
}

function recentDate(daysAgo) {
  return new Date(Date.now() - daysAgo * DAY).toISOString().slice(0, 10)
}

function stubResize() {
  let notifyResize
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback) { notifyResize = callback }
    observe() {}
    disconnect() {}
  })
  return () => notifyResize()
}

function mountChart(history, { width = 375, height = 800 } = {}) {
  const wrapper = mount(BalanceChart, { props: { balanceHistory: history } })
  const viewport = wrapper.get('.graph-scroll').element
  let viewportWidth = width
  let viewportHeight = height
  Object.defineProperty(viewport, 'clientWidth', { configurable: true, get: () => viewportWidth })
  Object.defineProperty(viewport, 'clientHeight', { configurable: true, get: () => viewportHeight })
  return {
    wrapper,
    resize(nextWidth = viewportWidth, nextHeight = viewportHeight) {
      viewportWidth = nextWidth
      viewportHeight = nextHeight
    }
  }
}

function chartWidth(wrapper) {
  return wrapper.get('.graph-container').element.style.width
}

it('sizes the chart from distinct transaction dates instead of viewport height', async () => {
  const notifyResize = stubResize()
  const { wrapper, resize } = mountChart(historyFor([recentDate(0)]), { width: 375, height: 1500 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')

  // 1〜2日分の点では縦長画面だけを理由に横スクロールしない。
  await wrapper.setProps({ balanceHistory: historyFor([recentDate(1), recentDate(0)]) })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')

  // スマホ幅で約5日分はスクロールなしで見える。
  const fiveDates = [40, 30, 20, 10, 0].map(recentDate)
  await wrapper.setProps({ balanceHistory: historyFor(fiveDates) })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')

  // 点が増えたときだけ点間隔に応じて横スクロールになる。
  const tenDates = Array.from({ length: 10 }, (_, index) => recentDate(90 - index * 10))
  await wrapper.setProps({ balanceHistory: historyFor(tenDates) })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('620px')
  expect(wrapper.get('.graph-scroll-hint').classes()).not.toContain('is-hidden')

  // デスクトップでは表示領域の幅をそのまま使う。
  resize(1400, 600)
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('1400px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')
  wrapper.unmount()
})

it('keeps very long histories renderable without dropping points', async () => {
  const notifyResize = stubResize()
  const dates = Array.from({ length: 1000 }, (_, index) => recentDate(1000 - index))
  const { wrapper } = mountChart(historyFor(dates), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  // jsdom は devicePixelRatio=1。canvas 面積予算 8M / 高さ 800 が上限になる。
  expect(chartWidth(wrapper)).toBe('10000px')
  expect(wrapper.get('.graph-scroll-hint').classes()).not.toContain('is-hidden')
  wrapper.unmount()
})

it('recalculates the required width when the display period changes', async () => {
  const notifyResize = stubResize()
  const dates = Array.from({ length: 12 }, (_, index) => recentDate(220 - index * 20))
  const { wrapper } = mountChart(historyFor(dates), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('740px')
  expect(wrapper.get('.graph-scroll-hint').classes()).not.toContain('is-hidden')

  await wrapper.get('#balance-chart-period').setValue('90')
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  expect(wrapper.get('.graph-scroll-hint').classes()).toContain('is-hidden')
  wrapper.unmount()
})

it('hides the vertical axis controls on compact viewports only', async () => {
  const notifyResize = stubResize()
  const { wrapper, resize } = mountChart(historyFor([recentDate(1), recentDate(0)]), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.find('.graph-y-controls').exists()).toBe(false)

  resize(1400, 600)
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.find('.graph-y-controls').exists()).toBe(true)
  expect(wrapper.find('[aria-label="縦軸を拡大"]').exists()).toBe(true)
  wrapper.unmount()
})

it('renders integer yen labels for the y axis', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const wrapper = mount(BalanceChart, {
    props: { balanceHistory: historyFor([recentDate(0)]) }
  })
  const chart = wrapper.get('.line-chart')
  expect(chart.attributes('data-tick-precision')).toBe('0')
  expect(chart.attributes('data-tick-label')).toBe('¥-2,006')
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
