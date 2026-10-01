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
        <div class="graph-y-controls" role="group" aria-label="縦軸の表示範囲">
          <span>縦軸:</span>
          <button type="button" aria-label="縦軸を拡大" title="縦軸を拡大" :disabled="!dataYRange" @click="zoomY(0.8)">＋</button>
          <button type="button" aria-label="縦軸を縮小" title="縦軸を縮小" :disabled="!dataYRange" @click="zoomY(1.25)">－</button>
          <button type="button" aria-label="縦軸を上へ移動" title="縦軸を上へ移動" :disabled="!dataYRange" @click="panY(1)">↑</button>
          <button type="button" aria-label="縦軸を下へ移動" title="縦軸を下へ移動" :disabled="!dataYRange" @click="panY(-1)">↓</button>
          <button type="button" aria-label="縦軸を元に戻す" title="縦軸を元に戻す" :disabled="!yViewport" @click="resetY">戻す</button>
        </div>
      </div>
      <p class="graph-gesture-hint">グラフ上ではドラッグで縦移動、ホイールで拡大・縮小できます</p>
      <p class="graph-scroll-hint" :class="{ 'is-hidden': !isHorizontallyScrollable }">グラフは左右にスクロールできます</p>
      <div ref="chartViewport" class="graph-scroll" :class="{ 'is-dragging': isDraggingY }" tabindex="0" role="region" aria-label="残高推移グラフ" @wheel="onChartWheel" @pointerdown="startYAxisPan" @pointermove="moveYAxisPan" @pointerup="stopYAxisPan" @pointercancel="stopYAxisPan" @lostpointercapture="stopYAxisPan">
        <div class="graph-container" :style="virtualChartWidth ? { width: virtualChartWidth } : null">
          <Line v-if="chartData" :data="chartData" :options="chartOptions" />
          <div v-else class="graph-empty">データがありません</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
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

const selectedPeriod = ref('all')
const chartViewport = ref(null)
const virtualChartWidth = ref(null)
const isHorizontallyScrollable = ref(false)
const yViewport = ref(null)
const isDraggingY = ref(false)
let chartResizeObserver
let dragStart

function updateChartDimensions() {
  const viewport = chartViewport.value
  if (!viewport) return

  const viewportWidth = viewport.clientWidth
  const viewportHeight = viewport.clientHeight
  if (!viewportWidth || !viewportHeight) return

  // 縦に長い画面でも、横幅をスクロール領域へ広げてグラフの比率を保つ。
  const width = Math.ceil(Math.max(viewportWidth, viewportHeight * 16 / 9))
  virtualChartWidth.value = `${width}px`
  isHorizontallyScrollable.value = width > viewportWidth + 1
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

// 日付ラベルの間引き（データが多すぎる場合）
function thinLabels(dates) {
  const maxLabels = 30
  if (dates.length <= maxLabels) return dates.map(d => formatDateLabel(d))

  const step = Math.ceil(dates.length / maxLabels)
  return dates.map((d, i) => {
    if (i % step === 0 || i === dates.length - 1) {
      return formatDateLabel(d)
    }
    return ''
  })
}

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
  const labels = thinLabels(history.dates)
  const datasets = accounts.map((acc, idx) => {
    const color = colorPalette[idx % colorPalette.length]
    const exactData = history.balances_exact?.[acc] || []
    return {
      label: acc,
      data: (history.balances[acc] || []).map((value, i) => Number(exactInteger(value, exactData[i]))),
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

  return { labels, datasets }
}

const dataYRange = computed(() => {
  let min = Infinity
  let max = -Infinity
  for (const dataset of chartData.value?.datasets || []) {
    for (const value of dataset.data) {
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

function panY(direction) {
  const range = yViewport.value || dataYRange.value
  if (!range) return
  const shift = (range.max - range.min) * 0.25 * direction
  setYRange(range.min + shift, range.max + shift)
}

function resetY() {
  yViewport.value = null
}

function onChartWheel(event) {
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

watch(chartData, resetY)

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
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
          return `${item.dataset.label}: ¥${formatExactInteger(item.raw, value)}`
        }
      }
    }
  },
  scales: {
    x: {
      ticks: {
        color: '#666',
        font: { size: 10 },
        maxRotation: 45,
        minRotation: 0,
        autoSkip: true,
        maxTicksLimit: 20,
      },
      grid: {
        color: 'rgba(0, 0, 0, 0.06)',
      }
    },
    y: {
      ticks: {
        color: '#666',
        font: { size: 11 },
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
          return '¥' + value.toLocaleString('ja-JP')
        },
      },
      grid: {
        color: 'rgba(0, 0, 0, 0.06)',
      },
      beginAtZero: false,
      ...(yViewport.value || {}),
    }
  }
}))

onMounted(() => {
  chartResizeObserver = new ResizeObserver(updateChartDimensions)
  if (chartViewport.value) chartResizeObserver.observe(chartViewport.value)
})

onUnmounted(() => {
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

.graph-period-control,
.graph-y-controls {
  display: flex;
  align-items: center;
  gap: .35rem;
}

.graph-y-controls { flex-wrap: wrap; }

.graph-y-controls span {
  color: #666;
  font-size: .85em;
}

.graph-y-controls button {
  min-width: 2.75rem;
  min-height: 2.75rem;
  border: 1px solid #ddd;
  border-radius: 8px;
  background: white;
  color: #333;
  cursor: pointer;
}

.graph-y-controls button:hover:not(:disabled),
.graph-y-controls button:focus-visible {
  border-color: #667eea;
  outline-color: #667eea;
}

.graph-y-controls button:disabled { opacity: .45; cursor: default; }

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
  -webkit-overflow-scrolling: touch;
  border-radius: 8px;
  cursor: grab;
}

.graph-scroll.is-dragging { cursor: grabbing; }

.graph-scroll:focus-visible {
  outline: 2px solid #667eea;
  outline-offset: 2px;
}

.graph-container {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  position: relative;
  background: #f8fafc;
  border-radius: 8px;
  padding: 12px;
}

.graph-scroll-hint {
  margin: 0 0 .4rem;
  color: #5e6664;
  font-size: .8rem;
}

.graph-scroll-hint.is-hidden { visibility: hidden; }

.graph-gesture-hint {
  margin: 0 0 .25rem;
  color: #5e6664;
  font-size: .8rem;
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

  .graph-y-controls { width: 100%; }

  .graph-gesture-hint { display: none; }
}
</style>
