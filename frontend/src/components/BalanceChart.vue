<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content graph-modal" @click.stop>
      <div class="graph-modal-header">
        <h3>残高推移グラフ</h3>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>
      <div class="graph-controls">
        <label class="graph-period-label">期間:</label>
        <select v-model="selectedPeriod" @change="updateChart" class="graph-period-select">
          <option value="all">全期間</option>
          <option value="365">過去1年</option>
          <option value="180">過去6ヶ月</option>
          <option value="90">過去3ヶ月</option>
          <option value="30">過去1ヶ月</option>
        </select>
      </div>
      <div class="graph-container" ref="chartContainer">
        <Line v-if="chartData" :data="chartData" :options="chartOptions" />
        <div v-else class="graph-empty">データがありません</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { Line } from 'vue-chartjs'
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
  for (const acc of history.accounts) {
    if (history.balances[acc]) {
      filteredBalances[acc] = history.balances[acc].slice(startIdx)
    }
  }

  return {
    accounts: history.accounts,
    dates: filteredDates,
    balances: filteredBalances
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
    return {
      label: acc,
      data: history.balances[acc] || [],
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
          const value = item.raw
          return `${item.dataset.label}: ¥${value.toLocaleString('ja-JP')}`
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
            return '¥' + (value / 10000).toFixed(0) + '万'
          }
          return '¥' + value.toLocaleString('ja-JP')
        },
      },
      grid: {
        color: 'rgba(0, 0, 0, 0.06)',
      },
      beginAtZero: false,
    }
  }
}))

function updateChart() {
  // computed で自動更新されるため特に処理不要
}
</script>

<style scoped>
.graph-modal {
  max-width: 900px;
  width: 95vw;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
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
  gap: 8px;
  margin-bottom: 12px;
  flex-shrink: 0;
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

.graph-container {
  flex: 1;
  min-height: 300px;
  max-height: 60vh;
  position: relative;
  background: #f8fafc;
  border-radius: 8px;
  padding: 8px;
}

.graph-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #999;
  font-size: 1.1em;
}

@media (max-width: 600px) {
  .graph-modal {
    width: 98vw;
    max-height: 90vh;
  }

  .graph-container {
    min-height: 250px;
  }
}
</style>
