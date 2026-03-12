<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content tag-chart-modal" @click.stop>
      <h3>タグ別分析</h3>

      <!-- 期間フィルタ -->
      <div class="filter-section">
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
      </div>

      <!-- パンくずリスト（ドリルダウン用） -->
      <div v-if="breadcrumbs.length > 0" class="breadcrumbs">
        <span class="breadcrumb-item" @click="drillUp(-1)">全体</span>
        <span v-for="(bc, i) in breadcrumbs" :key="i">
          <span class="breadcrumb-sep"> › </span>
          <span class="breadcrumb-item" @click="drillUp(i)">{{ bc.name }}</span>
        </span>
      </div>

      <!-- 円グラフ + 凡例 -->
      <div class="chart-body">
        <div v-if="currentData.length === 0" class="no-data">データがありません</div>
        <template v-else>
          <div class="chart-container">
            <canvas ref="chartCanvas"></canvas>
          </div>
          <div class="chart-legend">
            <div class="legend-total">
              合計: ¥{{ totalAmount.toLocaleString() }}
            </div>
            <div v-for="(item, i) in currentData" :key="i"
              class="legend-item"
              @click="drillDown(item)"
              :class="{ clickable: item.children && item.children.length > 0 }">
              <span class="legend-color" :style="{ background: chartColors[i % chartColors.length] }"></span>
              <span class="legend-name">{{ item.tag_name }}</span>
              <span class="legend-amount">¥{{ item.amount.toLocaleString() }}</span>
              <span class="legend-ratio">{{ (item.ratio * 100).toFixed(1) }}%</span>
              <span v-if="item.children && item.children.length > 0" class="legend-drill">▶</span>
            </div>
          </div>
        </template>
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
  '#667eea', '#764ba2', '#f093fb', '#4facfe', '#00f2fe',
  '#43e97b', '#fa709a', '#fee140', '#a18cd1', '#fbc2eb',
  '#ff9a9e', '#fad0c4', '#ffecd2', '#fcb69f', '#a1c4fd'
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

const totalAmount = computed(() => {
  return currentData.value.reduce((sum, item) => sum + item.amount, 0)
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
        borderWidth: 2,
        borderColor: '#fff'
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: true,
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(0,0,0,0.8)',
          titleFont: { size: 13 },
          bodyFont: { size: 12 },
          padding: 10,
          cornerRadius: 8,
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
  max-width: 560px;
  width: 95%;
  max-height: calc(100vh - 4rem);
  overflow-y: auto;
}

.tag-chart-modal h3 {
  margin-top: 0;
  margin-bottom: 1rem;
  color: #333;
  text-align: center;
}

.filter-section {
  margin-bottom: 1rem;
}

.period-filter {
  display: flex;
  gap: 6px;
  margin-bottom: 10px;
}

.period-btn {
  flex: 1;
  padding: 8px 4px;
  border: 1px solid #ddd;
  background: white;
  color: #666;
  border-radius: 8px;
  cursor: pointer;
  font-size: 0.9em;
  transition: all 0.2s;
}

.period-btn:hover {
  border-color: #667eea;
  color: #667eea;
}

.period-btn.active {
  background: #667eea;
  color: white;
  border-color: #667eea;
}

.date-navigator {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  margin-bottom: 10px;
}

.nav-btn {
  background: white;
  border: 1px solid #ddd;
  color: #333;
  border-radius: 6px;
  padding: 6px 12px;
  cursor: pointer;
  transition: all 0.2s;
}

.nav-btn:hover {
  border-color: #667eea;
  color: #667eea;
}

.period-label {
  font-size: 1em;
  font-weight: 500;
  color: #333;
}

.type-tabs {
  display: flex;
  gap: 6px;
}

.tab-btn {
  flex: 1;
  padding: 8px 4px;
  border: 1px solid #ddd;
  background: white;
  color: #666;
  border-radius: 8px;
  cursor: pointer;
  font-size: 0.9em;
  transition: all 0.2s;
}

.tab-btn:hover {
  border-color: #667eea;
}

.tab-btn.active {
  border-color: #667eea;
  background: rgba(102, 126, 234, 0.1);
  color: #667eea;
  font-weight: 500;
}

.breadcrumbs {
  margin-bottom: 10px;
  padding: 6px 10px;
  background: #f8f9fa;
  border-radius: 8px;
  font-size: 0.85em;
  color: #666;
}

.breadcrumb-item {
  cursor: pointer;
  color: #667eea;
}

.breadcrumb-item:hover {
  text-decoration: underline;
}

.breadcrumb-sep {
  color: #999;
}

.chart-body {
  display: flex;
  flex-direction: column;
  align-items: center;
}

.chart-container {
  position: relative;
  width: 100%;
  max-width: 300px;
  margin: 0 auto 16px;
}

.no-data {
  text-align: center;
  color: #999;
  padding: 40px 0;
  font-size: 0.95em;
}

.chart-legend {
  width: 100%;
}

.legend-total {
  text-align: right;
  font-weight: bold;
  color: #333;
  padding: 8px 10px;
  border-bottom: 2px solid #eee;
  margin-bottom: 4px;
  font-size: 0.95em;
}

.legend-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 8px;
  transition: background 0.2s;
}

.legend-item:hover {
  background: #f8f9fa;
}

.legend-item.clickable {
  cursor: pointer;
}

.legend-item.clickable:hover {
  background: rgba(102, 126, 234, 0.08);
}

.legend-color {
  width: 14px;
  height: 14px;
  border-radius: 4px;
  flex-shrink: 0;
}

.legend-name {
  flex: 1;
  font-size: 0.9em;
  color: #333;
  font-weight: 500;
}

.legend-amount {
  font-size: 0.9em;
  color: #333;
  font-weight: 500;
}

.legend-ratio {
  font-size: 0.8em;
  color: #999;
  min-width: 50px;
  text-align: right;
}

.legend-drill {
  font-size: 0.7em;
  color: #667eea;
}
</style>
