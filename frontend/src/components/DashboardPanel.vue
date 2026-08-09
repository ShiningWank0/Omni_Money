<template>
  <div class="dashboard">
    <!-- サマリーカード列 -->
    <div class="summary-grid">
      <div class="stat-card stat-balance">
        <div class="stat-label">現在の残高</div>
        <div class="stat-value">{{ formatCurrency(currentBalance) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">今月の収入</div>
        <div class="stat-value income">{{ formatCurrency(monthlyStats.income) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">今月の支出</div>
        <div class="stat-value expense">{{ formatCurrency(monthlyStats.expense) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">今月の収支</div>
        <div class="stat-value" :class="monthlyStats.net >= 0 ? 'income' : 'expense'">
          {{ (monthlyStats.net >= 0 ? '+' : '−') + formatCurrency(Math.abs(monthlyStats.net)) }}
        </div>
      </div>
    </div>

    <!-- チャート・口座別状態列 -->
    <div class="panel-grid">
      <div class="panel chart-panel" @click="$emit('open-balance-chart')" title="クリックで詳細グラフを表示">
        <div class="panel-title">残高推移（90日）</div>
        <div class="chart-wrap">
          <Line v-if="trendChartData" :data="trendChartData" :options="trendChartOptions" />
          <div v-else class="empty-hint">データがありません</div>
        </div>
      </div>
      <div class="panel chart-panel">
        <div class="panel-title">月別収支（6ヶ月）</div>
        <div class="chart-wrap">
          <Bar v-if="monthlyChartData" :data="monthlyChartData" :options="monthlyChartOptions" />
          <div v-else class="empty-hint">データがありません</div>
        </div>
      </div>
      <div class="panel account-panel">
        <div class="panel-title">口座別残高</div>
        <div class="account-list">
          <div v-for="acc in accountBalances" :key="acc.name" class="account-row">
            <span class="account-name">
              {{ acc.name }}
              <span v-if="acc.isCredit" class="credit-badge">カード</span>
            </span>
            <span class="account-balance" :class="{ 'credit-usage': acc.isCredit }">
              {{ acc.isCredit ? '利用額 ' + formatCurrency(Math.abs(acc.balance)) : formatCurrency(acc.balance) }}
            </span>
          </div>
          <div v-if="accountBalances.length === 0" class="empty-hint">口座がありません</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { Line, Bar } from 'vue-chartjs'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  Filler,
  Tooltip
} from 'chart.js'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, BarElement, Filler, Tooltip)

const props = defineProps({
  transactions: { type: Array, default: () => [] },
  creditCardItems: { type: Array, default: () => [] },
  balanceHistory: { type: Object, default: null },
  currentBalance: { type: Number, default: 0 }
})

defineEmits(['open-balance-chart'])

function formatCurrency(value) {
  if (value == null) return '¥0'
  return '¥' + value.toLocaleString('ja-JP')
}

// 取引の口座名（サーバーモード/Wailsモードでフィールド名が異なる場合に対応）
function accountOf(tx) {
  return tx.account || tx.fundItem
}

// ローカルタイムゾーンでYYYY-MMを返す(toISOStringはUTCのため月境界がずれる)
function localMonthKey(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

// 今月の収入・支出・収支
const monthlyStats = computed(() => {
  const thisMonth = localMonthKey(new Date())
  let income = 0
  let expense = 0
  for (const tx of props.transactions) {
    if (!tx.date || !tx.date.startsWith(thisMonth)) continue
    if (tx.type === 'income') income += tx.amount
    else expense += tx.amount
  }
  return { income, expense, net: income - expense }
})

// 口座別の最新残高
const accountBalances = computed(() => {
  const latest = {}
  const sorted = [...props.transactions].sort((a, b) => {
    const diff = new Date(a.date) - new Date(b.date)
    return diff !== 0 ? diff : a.id - b.id
  })
  for (const tx of sorted) {
    latest[accountOf(tx)] = tx.balance
  }
  return Object.keys(latest).sort().map(name => ({
    name,
    balance: latest[name],
    isCredit: props.creditCardItems.includes(name)
  }))
})

// 残高推移（全口座合計、直近90日）
const trendChartData = computed(() => {
  const h = props.balanceHistory
  if (!h || !h.dates || h.dates.length === 0) return null
  const start = Math.max(0, h.dates.length - 90)
  const dates = h.dates.slice(start)
  const totals = dates.map((_, i) =>
    h.accounts.reduce((sum, acc) => sum + (h.balances[acc]?.[start + i] ?? 0), 0)
  )
  return {
    labels: dates.map(d => d.slice(5)),
    datasets: [{
      data: totals,
      borderColor: '#667eea',
      backgroundColor: 'rgba(102, 126, 234, 0.15)',
      fill: true,
      pointRadius: 0,
      borderWidth: 2,
      tension: 0.3
    }]
  }
})

const trendChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: { legend: { display: false } },
  scales: {
    x: { ticks: { maxTicksLimit: 6, font: { size: 10 } }, grid: { display: false } },
    y: { ticks: { maxTicksLimit: 4, font: { size: 10 }, callback: v => '¥' + v.toLocaleString('ja-JP') } }
  },
  interaction: { intersect: false, mode: 'index' }
}

// 月別収支（直近6ヶ月）
const monthlyChartData = computed(() => {
  if (props.transactions.length === 0) return null
  const months = []
  const now = new Date()
  for (let i = 5; i >= 0; i--) {
    months.push(localMonthKey(new Date(now.getFullYear(), now.getMonth() - i, 1)))
  }
  const income = months.map(() => 0)
  const expense = months.map(() => 0)
  for (const tx of props.transactions) {
    if (!tx.date) continue
    const idx = months.indexOf(tx.date.slice(0, 7))
    if (idx < 0) continue
    if (tx.type === 'income') income[idx] += tx.amount
    else expense[idx] += tx.amount
  }
  return {
    labels: months.map(m => Number(m.slice(5)) + '月'),
    datasets: [
      { label: '収入', data: income, backgroundColor: 'rgba(40, 167, 69, 0.7)', borderRadius: 4 },
      { label: '支出', data: expense, backgroundColor: 'rgba(220, 53, 69, 0.7)', borderRadius: 4 }
    ]
  }
})

const monthlyChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: { legend: { display: false } },
  scales: {
    x: { ticks: { font: { size: 10 } }, grid: { display: false } },
    y: { ticks: { maxTicksLimit: 4, font: { size: 10 }, callback: v => '¥' + v.toLocaleString('ja-JP') } }
  }
}
</script>

<style scoped>
.dashboard {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding-bottom: 1rem;
  border-bottom: 1px solid #eee;
  margin-bottom: 1rem;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 0.75rem;
}

.stat-card {
  background: rgba(255, 255, 255, 0.7);
  border: 1px solid rgba(102, 126, 234, 0.15);
  border-radius: 14px;
  padding: 0.75rem 1rem;
  text-align: center;
}

.stat-balance {
  background: linear-gradient(135deg, rgba(102, 126, 234, 0.12) 0%, rgba(118, 75, 162, 0.12) 100%);
  border-color: rgba(102, 126, 234, 0.3);
}

.stat-label {
  font-size: 0.85rem;
  color: #666;
  margin-bottom: 0.25rem;
}

.stat-value {
  font-size: 1.5rem;
  font-weight: bold;
  color: #333;
  white-space: nowrap;
}

.stat-balance .stat-value {
  font-size: 1.7rem;
}

.stat-value.income {
  color: #28a745;
}

.stat-value.expense {
  color: #dc3545;
}

.panel-grid {
  display: grid;
  grid-template-columns: 1.2fr 1fr 0.8fr;
  gap: 0.75rem;
}

.panel {
  background: rgba(255, 255, 255, 0.7);
  border: 1px solid rgba(0, 0, 0, 0.06);
  border-radius: 14px;
  padding: 0.75rem 1rem;
  min-width: 0;
}

.chart-panel {
  cursor: pointer;
}

.panel-title {
  font-size: 0.85rem;
  font-weight: bold;
  color: #555;
  margin-bottom: 0.4rem;
}

.chart-wrap {
  height: 150px;
  position: relative;
}

.account-list {
  height: 150px;
  overflow-y: auto;
}

.account-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.35rem 0;
  border-bottom: 1px solid #f0f0f0;
  font-size: 0.9rem;
}

.account-row:last-child {
  border-bottom: none;
}

.account-name {
  color: #444;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.credit-badge {
  font-size: 0.7rem;
  background: rgba(118, 75, 162, 0.15);
  color: #764ba2;
  border-radius: 6px;
  padding: 0.05rem 0.4rem;
  margin-left: 0.3rem;
}

.account-balance {
  font-weight: bold;
  color: #333;
  white-space: nowrap;
}

.credit-usage {
  color: #764ba2;
  font-size: 0.85rem;
}

.empty-hint {
  color: #999;
  font-size: 0.85rem;
  text-align: center;
  padding-top: 2.5rem;
}

/* タブレット: パネルを2列に */
@media (max-width: 1100px) {
  .panel-grid {
    grid-template-columns: 1fr 1fr;
  }

  .account-panel {
    grid-column: span 2;
  }

  .account-list {
    height: auto;
    max-height: 120px;
  }
}

/* モバイル: サマリーは2x2、チャートは非表示（メニューのモーダルから閲覧可能） */
@media (max-width: 768px) {
  .summary-grid {
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }

  .stat-value {
    font-size: 1.15rem;
  }

  .stat-balance .stat-value {
    font-size: 1.3rem;
  }

  .panel-grid {
    display: none;
  }
}
</style>
