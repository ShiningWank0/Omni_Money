<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content tag-chart-modal" @click.stop>
      <div class="tag-chart-header">
        <h3>タグ別分析</h3>
        <button class="close-btn" @click="$emit('close')">×</button>
      </div>

      <!-- 期間フィルタ -->
      <div class="period-filter">
        <button v-for="p in periods" :key="p.value"
          :class="['period-btn', { active: periodMode === p.value }]"
          @click="setPeriodMode(p.value)">
          {{ p.label }}
        </button>
      </div>

      <div v-if="periodMode !== 'all'" class="date-navigator">
        <button class="nav-btn" @click="navigatePeriod(-1)">◀</button>
        <span class="period-label">{{ currentPeriodLabel }}</span>
        <button class="nav-btn" @click="navigatePeriod(1)">▶</button>
      </div>

      <!-- 収入/支出タブ -->
      <div class="type-tabs">
        <button :class="['tab-btn', { active: chartType === 'expense' }]" @click="chartType = 'expense'">支出</button>
        <button :class="['tab-btn', { active: chartType === 'income' }]" @click="chartType = 'income'">収入</button>
      </div>

      <!-- パンくずリスト（ドリルダウン用） -->
      <div v-if="breadcrumbs.length > 0" class="breadcrumbs">
        <span class="breadcrumb-item" @click="drillUp(-1)">全体</span>
        <span v-for="(bc, i) in breadcrumbs" :key="i">
          <span class="breadcrumb-sep"> › </span>
          <span class="breadcrumb-item" @click="drillUp(i)">{{ bc.name }}</span>
        </span>
      </div>

      <!-- 円グラフ -->
      <div class="chart-container">
        <canvas ref="chartCanvas"></canvas>
        <div v-if="currentData.length === 0" class="no-data">データがありません</div>
      </div>

      <!-- 凡例 -->
      <div class="chart-legend" v-if="currentData.length > 0">
        <div v-for="(item, i) in currentData" :key="i"
          class="legend-item"
          @click="drillDown(item)"
          :style="{ cursor: item.children && item.children.length > 0 ? 'pointer' : 'default' }">
          <span class="legend-color" :style="{ background: chartColors[i % chartColors.length] }"></span>
          <span class="legend-name">{{ item.tag_name }}</span>
          <span class="legend-amount">¥{{ item.amount.toLocaleString() }}</span>
          <span class="legend-ratio">{{ (item.ratio * 100).toFixed(1) }}%</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { getTagSummary } from '../utils/api'
import Chart from 'chart.js/auto'

const props = defineProps({
  creditCardItems: { type: Array, default: () => [] }
})

const emit = defineEmits(['close'])

const chartCanvas = ref(null)
let chartInstance = null

const periodMode = ref('all')
const chartType = ref('expense')
const periodOffset = ref(0)
const summaryData = ref([])
const breadcrumbs = ref([])

const periods = [
  { value: 'all', label: '通期' },
  { value: 'year', label: '年' },
  { value: 'month', label: '月' },
  { value: 'day', label: '日' }
]

const chartColors = [
  '#FF6384', '#36A2EB', '#FFCE56', '#4BC0C0', '#9966FF',
  '#FF9F40', '#FF6B6B', '#48BB78', '#ED64A6', '#667EEA',
  '#F6AD55', '#68D391', '#FC8181', '#63B3ED', '#B794F4'
]

const currentPeriodLabel = computed(() => {
  const now = new Date()
  switch (periodMode.value) {
    case 'year': {
      const y = now.getFullYear() + periodOffset.value
      return `${y}年`
    }
    case 'month': {
      const d = new Date(now.getFullYear(), now.getMonth() + periodOffset.value, 1)
      return `${d.getFullYear()}年${d.getMonth() + 1}月`
    }
    case 'day': {
      const d = new Date(now)
      d.setDate(d.getDate() + periodOffset.value)
      return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`
    }
    default:
      return '全期間'
  }
})

function getDateRange() {
  const now = new Date()
  let start = '', end = ''
  switch (periodMode.value) {
    case 'year': {
      const y = now.getFullYear() + periodOffset.value
      start = `${y}-01-01`
      end = `${y}-12-31`
      break
    }
    case 'month': {
      const d = new Date(now.getFullYear(), now.getMonth() + periodOffset.value, 1)
      start = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-01`
      const lastDay = new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate()
      end = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(lastDay).padStart(2, '0')}`
      break
    }
    case 'day': {
      const d = new Date(now)
      d.setDate(d.getDate() + periodOffset.value)
      const ds = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
      start = ds
      end = ds
      break
    }
  }
  return { start, end }
}

function setPeriodMode(mode) {
  periodMode.value = mode
  periodOffset.value = 0
  breadcrumbs.value = []
}

function navigatePeriod(dir) {
  periodOffset.value += dir
}

const currentData = computed(() => {
  if (breadcrumbs.value.length === 0) return summaryData.value
  let data = summaryData.value
  for (const bc of breadcrumbs.value) {
    const found = data.find(d => d.tag_id === bc.id)
    if (found && found.children) {
      data = found.children
    } else {
      return []
    }
  }
  return data
})

function drillDown(item) {
  if (!item.children || item.children.length === 0) return
  breadcrumbs.value.push({ id: item.tag_id, name: item.tag_name })
}

function drillUp(index) {
  if (index === -1) {
    breadcrumbs.value = []
  } else {
    breadcrumbs.value = breadcrumbs.value.slice(0, index + 1)
  }
}

async function loadData() {
  const { start, end } = getDateRange()
  try {
    summaryData.value = await getTagSummary(chartType.value, start, end)
  } catch (e) {
    summaryData.value = []
  }
  breadcrumbs.value = []
}

function renderChart() {
  if (!chartCanvas.value) return
  if (chartInstance) {
    chartInstance.destroy()
  }

  const data = currentData.value
  if (data.length === 0) return

  chartInstance = new Chart(chartCanvas.value, {
    type: 'pie',
    data: {
      labels: data.map(d => d.tag_name),
      datasets: [{
        data: data.map(d => d.amount),
        backgroundColor: data.map((_, i) => chartColors[i % chartColors.length]),
        borderWidth: 1,
        borderColor: 'rgba(0,0,0,0.2)'
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: true,
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            label: (ctx) => {
              const item = data[ctx.dataIndex]
              return `${item.tag_name}: ¥${item.amount.toLocaleString()} (${(item.ratio * 100).toFixed(1)}%)`
            }
          }
        }
      },
      onClick: (evt, elements) => {
        if (elements.length > 0) {
          const idx = elements[0].index
          drillDown(data[idx])
        }
      }
    }
  })
}

watch([periodMode, periodOffset, chartType], () => {
  loadData()
})

watch(currentData, () => {
  nextTick(() => renderChart())
})

onMounted(() => {
  loadData()
})

onUnmounted(() => {
  if (chartInstance) chartInstance.destroy()
})
</script>

<style scoped>
.tag-chart-modal {
  max-width: 520px;
  width: 95%;
  max-height: 85vh;
  overflow-y: auto;
}
.tag-chart-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.tag-chart-header h3 {
  margin: 0;
}
.close-btn {
  background: none;
  border: none;
  color: #e0e0e0;
  font-size: 1.5em;
  cursor: pointer;
}
.period-filter {
  display: flex;
  gap: 4px;
  margin-bottom: 8px;
}
.period-btn {
  flex: 1;
  padding: 6px;
  border: 1px solid rgba(255,255,255,0.2);
  background: rgba(0,0,0,0.2);
  color: #aaa;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.85em;
  transition: all 0.2s;
}
.period-btn.active {
  background: rgba(106, 168, 79, 0.6);
  color: white;
  border-color: rgba(106, 168, 79, 0.8);
}
.date-navigator {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  margin-bottom: 8px;
}
.nav-btn {
  background: none;
  border: 1px solid rgba(255,255,255,0.2);
  color: #e0e0e0;
  border-radius: 4px;
  padding: 4px 10px;
  cursor: pointer;
}
.period-label {
  font-size: 0.95em;
  color: #e0e0e0;
}
.type-tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 10px;
}
.tab-btn {
  flex: 1;
  padding: 6px;
  border: 1px solid rgba(255,255,255,0.2);
  background: rgba(0,0,0,0.2);
  color: #aaa;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}
.tab-btn.active {
  background: rgba(54, 162, 235, 0.5);
  color: white;
  border-color: rgba(54, 162, 235, 0.8);
}
.breadcrumbs {
  margin-bottom: 8px;
  font-size: 0.85em;
  color: #aaa;
}
.breadcrumb-item {
  cursor: pointer;
  color: #63B3ED;
}
.breadcrumb-item:hover {
  text-decoration: underline;
}
.breadcrumb-sep {
  color: #666;
}
.chart-container {
  position: relative;
  margin: 8px auto;
  max-width: 280px;
}
.no-data {
  text-align: center;
  color: #888;
  padding: 40px;
}
.chart-legend {
  margin-top: 12px;
}
.legend-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 8px;
  border-radius: 6px;
  transition: background 0.2s;
}
.legend-item:hover {
  background: rgba(255,255,255,0.05);
}
.legend-color {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  flex-shrink: 0;
}
.legend-name {
  flex: 1;
  font-size: 0.9em;
}
.legend-amount {
  font-size: 0.85em;
  color: #e0e0e0;
}
.legend-ratio {
  font-size: 0.8em;
  color: #aaa;
  min-width: 45px;
  text-align: right;
}
</style>
