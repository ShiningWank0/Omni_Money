import { mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import BalanceChart from '../../src/components/BalanceChart.vue'

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    template: `<div class="line-chart"
      :data-y-min="options.scales.y.min"
      :data-y-max="options.scales.y.max"
      :data-x-min="options.scales.x.min"
      :data-x-max="options.scales.x.max"
      :data-first-date="options.scales.x.ticks.callback(0)"
      :data-first-amount="data.datasets[0]?.data[0]?.y"
      :data-tooltip-date="options.plugins.tooltip.callbacks.title([{ dataIndex: 0 }])"
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
  vi.stubGlobal('requestAnimationFrame', callback => {
    queueMicrotask(callback)
    return 1
  })
  vi.stubGlobal('cancelAnimationFrame', () => {})
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
  return wrapper.get('.graph-track').element.style.width
}

it('sizes the chart from distinct transaction dates instead of viewport height', async () => {
  const notifyResize = stubResize()
  const { wrapper, resize } = mountChart(historyFor([recentDate(0)]), { width: 375, height: 1500 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  expect(wrapper.get('.graph-container').element.style.width).toBe('375px')

  // 1〜2日分の点では縦長画面だけを理由に横スクロールしない。
  await wrapper.setProps({ balanceHistory: historyFor([recentDate(1), recentDate(0)]) })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')

  // スマホ幅で約5日分はスクロールなしで見える。
  const fiveDates = [40, 30, 20, 10, 0].map(recentDate)
  await wrapper.setProps({ balanceHistory: historyFor(fiveDates) })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')

  // 点が増えたときだけ点間隔に応じて横スクロールになる。
  const tenDates = Array.from({ length: 10 }, (_, index) => recentDate(90 - index * 10))
  await wrapper.setProps({ balanceHistory: historyFor(tenDates) })
  notifyResize()
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('620px')
  expect(wrapper.get('.graph-scroll').element.scrollLeft).toBe(245)
  expect(Number(wrapper.get('.line-chart').attributes('data-x-max'))).toBe(9)

  // デスクトップでは表示領域の幅をそのまま使う。
  resize(1400, 600)
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('1400px')
  wrapper.unmount()
})

it('keeps every transaction date at the minimum spacing with a viewport-sized canvas', async () => {
  const notifyResize = stubResize()
  const dates = Array.from({ length: 1000 }, (_, index) => recentDate(1000 - index))
  const { wrapper } = mountChart(historyFor(dates), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('60020px')
  expect(wrapper.get('.graph-container').element.style.width).toBe('375px')
  expect(wrapper.get('.graph-scroll').element.scrollLeft).toBe(59645)
  expect(Number(wrapper.get('.line-chart').attributes('data-x-max'))).toBe(999)
  wrapper.get('.graph-scroll').element.scrollLeft = 0
  await wrapper.get('.graph-scroll').trigger('scroll')
  await Promise.resolve()
  await wrapper.vm.$nextTick()
  expect(Number(wrapper.get('.line-chart').attributes('data-x-min'))).toBe(0)
  expect(Number(wrapper.get('.line-chart').attributes('data-x-max'))).toBeCloseTo(295 / 60)
  expect(wrapper.get('.line-chart').attributes('data-first-date')).toBe(
    `${Number(dates[0].slice(5, 7))}/${Number(dates[0].slice(8, 10))}`
  )
  expect(wrapper.get('.line-chart').attributes('data-first-amount')).toBe('100')
  expect(wrapper.get('.line-chart').attributes('data-tooltip-date')).toBe(dates[0])
  wrapper.unmount()

  // PC幅でも同じ点間隔を使い、最初は最新側を表示する。
  const desktop = mountChart(historyFor(dates), { width: 1400, height: 800 })
  notifyResize()
  await desktop.wrapper.vm.$nextTick()
  await desktop.wrapper.vm.$nextTick()
  expect(chartWidth(desktop.wrapper)).toBe('60020px')
  expect(desktop.wrapper.get('.graph-container').element.style.width).toBe('1400px')
  expect(desktop.wrapper.get('.graph-scroll').element.scrollLeft).toBe(58620)
  desktop.wrapper.unmount()
})

it('recalculates the required width when the display period changes', async () => {
  const notifyResize = stubResize()
  const dates = Array.from({ length: 12 }, (_, index) => recentDate(220 - index * 20))
  const { wrapper } = mountChart(historyFor(dates), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('740px')

  await wrapper.get('#balance-chart-period').setValue('90')
  await wrapper.vm.$nextTick()
  expect(chartWidth(wrapper)).toBe('375px')
  wrapper.unmount()
})

it('does not render dedicated vertical axis buttons at any viewport width', async () => {
  const notifyResize = stubResize()
  const { wrapper, resize } = mountChart(historyFor([recentDate(1), recentDate(0)]), { width: 375, height: 800 })
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.find('.graph-y-controls').exists()).toBe(false)

  resize(1400, 600)
  notifyResize()
  await wrapper.vm.$nextTick()
  expect(wrapper.find('.graph-y-controls').exists()).toBe(false)
  expect(wrapper.find('[aria-label="縦軸を拡大"]').exists()).toBe(false)
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

it('zooms and pans the vertical chart range with mouse gestures', async () => {
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
  expect(chart().attributes('data-y-max')).toBe('106')
  pointer('pointerup', 110)

  const wheel = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -100 })
  graph.dispatchEvent(wheel)
  await wrapper.vm.$nextTick()
  expect(wheel.defaultPrevented).toBe(true)
  expect(Number(chart().attributes('data-y-min'))).toBeGreaterThan(96)
  expect(Number(chart().attributes('data-y-max'))).toBeLessThan(106)
  await wrapper.setProps({ balanceHistory: historyFor([recentDate(0)]) })
  expect(chart().attributes('data-y-min')).toBeUndefined()
  wrapper.unmount()
})

it('keeps horizontal touch scrolling native and pans or pinches the vertical range', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const wrapper = mount(BalanceChart, {
    props: { balanceHistory: { accounts: ['test'], dates: ['2026-10-01'], balances: { test: [100] } } }
  })
  const chart = () => wrapper.get('.line-chart')
  const graph = wrapper.get('.graph-scroll').element
  Object.defineProperty(graph, 'clientHeight', { value: 600 })
  const touch = (type, positions) => {
    const event = new Event(type, { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'touches', { value: positions.map(([clientX, clientY]) => ({ clientX, clientY })) })
    graph.dispatchEvent(event)
    return event
  }

  touch('touchstart', [[200, 50]])
  const horizontal = touch('touchmove', [[100, 52]])
  expect(horizontal.defaultPrevented).toBe(false)
  expect(chart().attributes('data-y-min')).toBeUndefined()
  touch('touchend', [])

  touch('touchstart', [[20, 50]])
  const vertical = touch('touchmove', [[22, 110]])
  await wrapper.vm.$nextTick()
  expect(vertical.defaultPrevented).toBe(true)
  expect(chart().attributes('data-y-min')).toBe('96')
  expect(chart().attributes('data-y-max')).toBe('106')
  touch('touchend', [])

  touch('touchstart', [[100, 100], [200, 100]])
  const pinch = touch('touchmove', [[80, 100], [220, 100]])
  await wrapper.vm.$nextTick()
  expect(pinch.defaultPrevented).toBe(true)
  const afterPinch = Number(chart().attributes('data-y-min'))
  expect(afterPinch).toBeGreaterThan(96)
  expect(Number(chart().attributes('data-y-max'))).toBeLessThan(106)
  touch('touchend', [])

  touch('touchstart', [[100, 100], [200, 100]])
  const twoFingerPan = touch('touchmove', [[100, 160], [200, 160]])
  await wrapper.vm.$nextTick()
  expect(twoFingerPan.defaultPrevented).toBe(true)
  expect(Number(chart().attributes('data-y-min'))).toBeGreaterThan(afterPinch)
  touch('touchend', [])
  wrapper.unmount()
})
