<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content graph-modal" @click.stop>
      <div class="graph-modal-header">
        <h3>残高推移グラフ</h3>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>
      <div class="graph-controls">
        <div class="graph-period-control">
          <label for="balance-chart-period" class="graph-period-label">期間:</label>
          <select id="balance-chart-period" v-model="selectedPeriod" class="graph-period-select">
            <option value="all">全期間</option>
            <option value="365">過去1年</option>
            <option value="180">過去6ヶ月</option>
            <option value="90">過去3ヶ月</option>
            <option value="30">過去1ヶ月</option>
          </select>
        </div>
      </div>
      <div ref="chartViewport" class="graph-scroll" :class="{ 'is-dragging': isDraggingY }" tabindex="0" role="region" aria-label="残高推移グラフ" @scroll="onChartScroll" @wheel="onChartWheel" @pointerdown="startYAxisPan" @pointermove="moveYAxisPan" @pointerup="stopYAxisPan" @pointercancel="stopYAxisPan" @lostpointercapture="stopYAxisPan" @touchstart="startTouchGesture" @touchmove="moveTouchGesture" @touchend="endTouchGesture" @touchcancel="endTouchGesture">
        <div class="graph-track" :style="virtualChartWidth ? { width: virtualChartWidth } : null">
          <div class="graph-container" :style="chartWindowWidth ? { width: chartWindowWidth } : null">
            <Line v-if="chartData" :data="chartData" :options="chartOptions" :plugins="chartPlugins" />
            <div v-else class="graph-empty">データがありません</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { Line } from 'vue-chartjs'
import { exactInteger, formatExactInteger } from '../utils/exactAmount'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler
} from 'chart.js'

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler
)

const props = defineProps({
  balanceHistory: Object,
  creditCardItems: { type: Array, default: () => [] }
})

defineEmits(['close'])

// 点間の最小間隔とY軸ラベル・左右余白の見積もり。横幅は画面高ではなく
// 取引のある異なる日付の数だけで決める（日付の暦上の空白は追加しない）。
const CHART_POINT_SPACING = 60
const CHART_VERTICAL_SCALE_WIDTH = 56
const CHART_HORIZONTAL_MARGIN = 24
const selectedPeriod = ref('all')
const chartViewport = ref(null)
const virtualChartWidth = ref(null)
const chartWindowWidth = ref(null)
const isHorizontallyScrollable = ref(false)
const chartScrollLeft = ref(0)
const chartPlotLeft = ref(CHART_VERTICAL_SCALE_WIDTH)
const chartPlotWidth = ref(0)
const chartPlotRightMargin = ref(CHART_HORIZONTAL_MARGIN)
const yViewport = ref(null)
const isDraggingY = ref(false)
let chartResizeObserver
let dragStart
let touchGesture
let scrollFrame
let layoutRevision = 0
let isUnmounted = false
let alignLatestPending = true
let measuredChartWidth = 0

// Chart.js の canvas は常に表示領域幅に抑える。横スクロール用の track だけを
// 日付数に応じて伸ばし、x 軸の表示範囲をスクロール位置へ同期する。
const chartPlugins = [{
  id: 'balanceChartPlotArea',
  afterLayout(chart) {
    // x 軸の範囲変更でも afterLayout は呼ばれる。各幅で一度だけ測り、
    // 測定値→オプション更新→再測定の循環を起こさない。
    if (Math.round(chart.width) === measuredChartWidth) return
    measuredChartWidth = Math.round(chart.width)
    const left = Math.round(chart.chartArea.left)
    const width = Math.round(chart.chartArea.right - chart.chartArea.left)
    const rightMargin = Math.round(chart.width - chart.chartArea.right)
    if (!Number.isFinite(left) || !Number.isFinite(width) || width < 1 || !Number.isFinite(rightMargin)) return
    if (left === chartPlotLeft.value && width === chartPlotWidth.value && rightMargin === chartPlotRightMargin.value) return
    queueMicrotask(() => {
      if (isUnmounted) return
      chartPlotLeft.value = left
      chartPlotWidth.value = width
      chartPlotRightMargin.value = rightMargin
      updateChartDimensions()
    })
  }
}]

function onChartScroll() {
  if (scrollFrame != null) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = null
    chartScrollLeft.value = chartViewport.value?.scrollLeft || 0
  })
}

function updateChartDimensions(alignLatest = false) {
  const viewport = chartViewport.value
  if (!viewport) return
  if (alignLatest) alignLatestPending = true

  const viewportWidth = viewport.clientWidth
  const viewportHeight = viewport.clientHeight
  if (!viewportWidth || !viewportHeight) return

  const previousWidth = Number.parseFloat(virtualChartWidth.value) || viewportWidth
  const previousViewportWidth = Number.parseFloat(chartWindowWidth.value) || viewportWidth
  const wasAtLatest = previousWidth - previousViewportWidth - viewport.scrollLeft <= 2
  const points = filteredHistory.value?.dates?.length || 0
  const width = Math.ceil(Math.max(
    viewportWidth,
    chartPlotLeft.value + CHART_POINT_SPACING * Math.max(0, points - 1) + chartPlotRightMargin.value
  ))
  chartWindowWidth.value = `${viewportWidth}px`
  virtualChartWidth.value = `${width}px`
  isHorizontallyScrollable.value = width > viewportWidth + 1
  const revision = ++layoutRevision
  nextTick(() => {
    if (isUnmounted || revision !== layoutRevision || !chartViewport.value) return
    if (alignLatestPending || wasAtLatest) {
      chartViewport.value.scrollLeft = Math.max(0, width - viewportWidth)
    }
    chartScrollLeft.value = chartViewport.value.scrollLeft
    alignLatestPending = false
  })
}

// 口座ごとの色パレット（グラスモーフィズムに合う色合い）
const colorPalette = [
  { border: 'rgba(100, 180, 255, 1)', bg: 'rgba(100, 180, 255, 0.15)' },
  { border: 'rgba(255, 130, 170, 1)', bg: 'rgba(255, 130, 170, 0.15)' },
  { border: 'rgba(130, 220, 160, 1)', bg: 'rgba(130, 220, 160, 0.15)' },
  { border: 'rgba(255, 200, 100, 1)', bg: 'rgba(255, 200, 100, 0.15)' },
  { border: 'rgba(180, 140, 255, 1)', bg: 'rgba(180, 140, 255, 0.15)' },
  { border: 'rgba(255, 160, 100, 1)', bg: 'rgba(255, 160, 100, 0.15)' },
]

// 期間でフィルタリングされたデータ
const filteredHistory = computed(() => {
  const history = props.balanceHistory
  if (!history || !history.dates || history.dates.length === 0) return null

  if (selectedPeriod.value === 'all') return history

  const days = parseInt(selectedPeriod.value)
  const cutoff = new Date()
  cutoff.setDate(cutoff.getDate() - days)
  const cutoffStr = cutoff.toISOString().slice(0, 10)

  const startIdx = history.dates.findIndex(d => d >= cutoffStr)
  if (startIdx < 0) return null

  const filteredDates = history.dates.slice(startIdx)
  const filteredBalances = {}
  const filteredBalancesExact = {}
  for (const acc of history.accounts) {
    if (history.balances[acc]) {
      filteredBalances[acc] = history.balances[acc].slice(startIdx)
      if (history.balances_exact?.[acc]) filteredBalancesExact[acc] = history.balances_exact[acc].slice(startIdx)
    }
  }

  return {
    accounts: history.accounts,
    dates: filteredDates,
    balances: filteredBalances,
    balances_exact: filteredBalancesExact
  }
})

function formatDateLabel(dateStr) {
  const parts = dateStr.split('-')
  if (parts.length >= 3) {
    return `${parseInt(parts[1])}/${parseInt(parts[2])}`
  }
  return dateStr
}

const chartData = computed(() => {
  const history = filteredHistory.value
  if (!history || !history.dates || history.dates.length === 0) return null

  // クレジットカード口座を除外
  const visibleAccounts = history.accounts.filter(
    acc => !props.creditCardItems.includes(acc)
  )
  if (visibleAccounts.length === 0 && history.accounts.length > 0) {
    // 全てクレジットカードの場合はそのまま表示
    return buildChartData(history, history.accounts)
  }
  return buildChartData(history, visibleAccounts.length > 0 ? visibleAccounts : history.accounts)
})

function buildChartData(history, accounts) {
  const datasets = accounts.map((acc, idx) => {
    const color = colorPalette[idx % colorPalette.length]
    const exactData = history.balances_exact?.[acc] || []
    return {
      label: acc,
      data: (history.balances[acc] || []).map((value, i) => ({ x: i, y: Number(exactInteger(value, exactData[i])) })),
      exactData,
      borderColor: color.border,
      backgroundColor: color.bg,
      fill: true,
      tension: 0.3,
      pointRadius: history.dates.length > 60 ? 0 : 3,
      pointHoverRadius: 5,
      borderWidth: 2,
    }
  })

  return { datasets }
}

const dataYRange = computed(() => {
  let min = Infinity
  let max = -Infinity
  for (const dataset of chartData.value?.datasets || []) {
    for (const point of dataset.data) {
      const value = point.y
      if (!Number.isFinite(value)) continue
      min = Math.min(min, value)
      max = Math.max(max, value)
    }
  }
  if (!Number.isFinite(min) || !Number.isFinite(max)) return null
  const span = max - min
  if (!Number.isFinite(span)) return null
  const padding = span > 0 ? span * 0.05 : Math.max(Math.abs(min) * 0.05, 1)
  const range = { min: min - padding, max: max + padding }
  return Number.isFinite(range.min) && Number.isFinite(range.max) ? range : null
})

function setYRange(min, max) {
  if (Number.isFinite(min) && Number.isFinite(max) && max > min) {
    yViewport.value = { min, max }
  }
}

function zoomY(factor) {
  const range = yViewport.value || dataYRange.value
  if (!range) return
  const center = range.min + (range.max - range.min) / 2
  const span = (range.max - range.min) * factor
  if (span <= Math.abs(center) * Number.EPSILON * 32) return
  setYRange(center - span / 2, center + span / 2)
}

function resetY() {
  yViewport.value = null
}

function onChartWheel(event) {
  if (event.shiftKey && isHorizontallyScrollable.value) {
    event.preventDefault()
    chartViewport.value.scrollLeft += event.deltaY
    onChartScroll()
    return
  }
  if (!dataYRange.value || Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return
  event.preventDefault()
  zoomY(Math.exp(Math.max(-100, Math.min(100, event.deltaY)) * 0.002))
}

function startYAxisPan(event) {
  if (event.button !== 0 || event.pointerType === 'touch' || !dataYRange.value) return
  dragStart = {
    pointerId: event.pointerId,
    x: event.clientX,
    y: event.clientY,
    range: { ...(yViewport.value || dataYRange.value) }
  }
  event.currentTarget.setPointerCapture(event.pointerId)
}

function moveYAxisPan(event) {
  if (!dragStart || event.pointerId !== dragStart.pointerId) return
  const dx = event.clientX - dragStart.x
  const dy = event.clientY - dragStart.y
  if (!isDraggingY.value) {
    if (Math.abs(dy) < 4 || Math.abs(dy) <= Math.abs(dx)) return
    isDraggingY.value = true
  }
  const height = chartViewport.value?.clientHeight
  if (!height) return
  const shift = dy / height * (dragStart.range.max - dragStart.range.min)
  setYRange(dragStart.range.min + shift, dragStart.range.max + shift)
}

function stopYAxisPan(event) {
  if (!dragStart || event.pointerId !== dragStart.pointerId) return
  dragStart = null
  isDraggingY.value = false
  if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
    event.currentTarget.releasePointerCapture(event.pointerId)
  }
}

function startTouchGesture(event) {
  const range = yViewport.value || dataYRange.value
  if (!range) return
  const touches = event.touches
  if (touches.length === 1) {
    touchGesture = { kind: 'single', mode: null, x: touches[0].clientX, y: touches[0].clientY, range: { ...range } }
  } else if (touches.length === 2) {
    const first = touches[0]
    const second = touches[1]
    touchGesture = {
      kind: 'pinch',
      distance: Math.hypot(second.clientX - first.clientX, second.clientY - first.clientY),
      centerY: (first.clientY + second.clientY) / 2,
      range: { ...range }
    }
  } else {
    touchGesture = null
  }
}

function moveTouchGesture(event) {
  if (!touchGesture || !dataYRange.value) return
  const touches = event.touches
  const height = chartViewport.value?.clientHeight
  if (!height) return
  if (touches.length === 1 && touchGesture.kind === 'single') {
    const dx = touches[0].clientX - touchGesture.x
    const dy = touches[0].clientY - touchGesture.y
    if (!touchGesture.mode) {
      if (Math.max(Math.abs(dx), Math.abs(dy)) < 6) return
      touchGesture.mode = Math.abs(dx) > Math.abs(dy) ? 'horizontal' : 'vertical'
    }
    if (touchGesture.mode === 'horizontal') return // Keep native horizontal scrolling and momentum.
    event.preventDefault()
    const span = touchGesture.range.max - touchGesture.range.min
    const shift = dy / height * span
    setYRange(touchGesture.range.min + shift, touchGesture.range.max + shift)
  } else if (touches.length === 2 && touchGesture.kind === 'pinch' && touchGesture.distance > 0) {
    event.preventDefault()
    const first = touches[0]
    const second = touches[1]
    const distance = Math.hypot(second.clientX - first.clientX, second.clientY - first.clientY)
    if (!distance) return
    const centerY = (first.clientY + second.clientY) / 2
    const range = touchGesture.range
    const center = (range.min + range.max) / 2
    const span = (range.max - range.min) * touchGesture.distance / distance
    if (span <= Math.abs(center) * Number.EPSILON * 32) return
    const shift = (centerY - touchGesture.centerY) / height * span
    setYRange(center - span / 2 + shift, center + span / 2 + shift)
  }
}

function endTouchGesture(event) {
  if (event.touches.length) startTouchGesture(event)
  else touchGesture = null
}

watch(chartData, resetY)

// 期間・履歴の変更時は最新の日付へ戻す。ResizeObserver は表示位置を保つ。
watch(filteredHistory, async () => {
  measuredChartWidth = 0
  await nextTick()
  updateChartDimensions(true)
})

const chartXRange = computed(() => {
  const points = filteredHistory.value?.dates?.length || 0
  if (points <= 1) return { min: -0.5, max: 0.5 }
  if (!isHorizontallyScrollable.value) return { min: 0, max: points - 1 }
  const min = chartScrollLeft.value / CHART_POINT_SPACING
  const width = chartPlotWidth.value || Math.max(1, (Number.parseFloat(chartWindowWidth.value) || 0) - CHART_VERTICAL_SCALE_WIDTH - CHART_HORIZONTAL_MARGIN)
  return { min, max: min + width / CHART_POINT_SPACING }
})

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  animation: false,
  parsing: false,
  interaction: {
    mode: 'index',
    intersect: false,
  },
  plugins: {
    legend: {
      position: 'top',
      labels: {
        color: '#333',
        font: { size: 12 },
        boxWidth: 20,
        padding: 12,
      },
    },
    tooltip: {
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      titleColor: '#fff',
      bodyColor: '#fff',
      borderColor: 'rgba(0, 0, 0, 0.1)',
      borderWidth: 1,
      padding: 10,
      cornerRadius: 8,
      callbacks: {
        title(items) {
          if (!items.length) return ''
          const history = filteredHistory.value
          if (!history) return ''
          const idx = items[0].dataIndex
          return history.dates[idx] || ''
        },
        label(item) {
          const value = item.dataset.exactData?.[item.dataIndex]
          return `${item.dataset.label}: ¥${formatExactInteger(item.parsed.y, value)}`
        }
      }
    }
  },
  scales: {
    x: {
      type: 'linear',
      min: chartXRange.value.min,
      max: chartXRange.value.max,
      ticks: {
        color: '#666',
        font: { size: 10 },
        maxRotation: 45,
        minRotation: 0,
        autoSkip: true,
        maxTicksLimit: 20,
        stepSize: 1,
        callback(value) {
          if (!Number.isInteger(value)) return ''
          const date = filteredHistory.value?.dates?.[value]
          return date ? formatDateLabel(date) : ''
        },
      },
      grid: {
        color: 'rgba(0, 0, 0, 0.06)',
      }
    },
    y: {
      ticks: {
        color: '#666',
        font: { size: 11 },
        precision: 0,
        callback(value) {
          if (Math.abs(value) >= 1000000) {
            return '¥' + (value / 1000000).toFixed(1) + 'M'
          }
          if (Math.abs(value) >= 10000) {
            return '¥' + (value / 10000).toLocaleString('ja-JP', {
              minimumFractionDigits: 0,
              maximumFractionDigits: 2,
            }) + '万'
          }
          return '¥' + value.toLocaleString('ja-JP', { maximumFractionDigits: 0 })
        },
      },
      grid: {
        color: 'rgba(0, 0, 0, 0.06)',
      },
      beginAtZero: false,
      suggestedMin: dataYRange.value?.min,
      suggestedMax: dataYRange.value?.max,
      ...(yViewport.value || {}),
    }
  }
}))

onMounted(() => {
  chartResizeObserver = new ResizeObserver(() => updateChartDimensions())
  if (chartViewport.value) chartResizeObserver.observe(chartViewport.value)
  updateChartDimensions(true)
})

onUnmounted(() => {
  isUnmounted = true
  if (scrollFrame != null) cancelAnimationFrame(scrollFrame)
  chartResizeObserver?.disconnect()
})
</script>

<style scoped>
.graph-modal {
  width: calc(100vw - 2rem);
  max-width: none;
  height: calc(100vh - 2rem);
  height: calc(100dvh - 2rem);
  max-height: none;
  padding: 1.5rem;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.graph-modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.graph-modal-header h3 {
  margin: 0;
  font-size: 1.1em;
  color: #333;
}

.close-btn {
  background: none;
  border: none;
  color: #999;
  font-size: 1.5em;
  cursor: pointer;
  padding: 0 4px;
  line-height: 1;
}

.close-btn:hover {
  color: #333;
}

.graph-controls {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
  flex-shrink: 0;
}

.graph-period-control {
  display: flex;
  align-items: center;
  gap: .35rem;
}

.graph-period-label {
  font-size: 0.9em;
  color: #666;
  font-weight: 500;
}

.graph-period-select {
  background: white;
  border: 1px solid #ddd;
  border-radius: 8px;
  color: #333;
  padding: 6px 10px;
  font-size: 0.85em;
  cursor: pointer;
}

.graph-period-select:focus {
  border-color: #667eea;
  outline: none;
}

.graph-scroll {
  flex: 1;
  min-height: 0;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  overscroll-behavior-inline: contain;
  touch-action: pan-x;
  -webkit-overflow-scrolling: touch;
  border-radius: 8px;
  cursor: grab;
}

.graph-scroll.is-dragging { cursor: grabbing; }

.graph-scroll:focus-visible {
  outline: 2px solid #667eea;
  outline-offset: 2px;
}

.graph-track {
  height: 100%;
  position: relative;
}

.graph-container {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  position: sticky;
  left: 0;
  background: #f8fafc;
  border-radius: 8px;
}

.graph-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #999;
  font-size: 1.1em;
}

@media (max-width: 700px) {
  .graph-modal {
    width: calc(100vw - 1rem);
    max-width: calc(100vw - 1rem);
    height: calc(100vh - 1rem);
    height: calc(100dvh - 1rem);
    max-height: calc(100vh - 1rem);
    max-height: calc(100dvh - 1rem);
    padding: 1rem;
  }

}
</style>
