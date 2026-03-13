<template>
  <div id="app">
    <!-- ヘッダーエリア -->
    <div class="card header">
      <div class="header-top">
        <div class="header-left">
          <div class="hamburger-menu" :class="{ 'menu-open': showMenu }" @click="toggleMenu">
            <span class="material-icons">menu</span>
          </div>
          <div class="project-selector" @click.stop="toggleAccountDropdown">
            <span class="chevron-anim">
              <span v-if="showAccountDropdown" key="down">▼</span>
              <span v-else key="up">▶</span>
            </span>
            <span>{{ store.selectedFundItemDisplay }}</span>
            <div v-if="showAccountDropdown" class="account-dropdown" @click.stop>
              <div class="fund-item-header">
                <button @click="store.toggleAllFundItems(); refreshData()" class="toggle-all-btn">
                  {{ store.selectedFundItems.length === store.actualFundItems.length ? '全解除' : '全選択' }}
                </button>
              </div>
              <div class="fund-item-list">
                <label v-for="fundItemName in store.actualFundItems" :key="fundItemName" class="fund-item-checkbox">
                  <input
                    type="checkbox"
                    :checked="store.selectedFundItems.includes(fundItemName)"
                    @change="store.toggleFundItem(fundItemName); refreshData()"
                  >
                  <span class="checkmark"></span>
                  <span class="fund-item-name">{{ fundItemName }}</span>
                </label>
              </div>
            </div>
          </div>
        </div>
        <div class="header-add-btn">
          <button class="add-btn" @click="showAddModal" title="新しい取引を追加">+</button>
        </div>
      </div>
      <div class="header-search">
        <div class="search-container">
          <input type="text" class="search-box" placeholder="項目名・メモで検索" v-model="store.searchQuery" @input="onSearchInput">
          <span class="search-icon">🔍</span>
        </div>
        <button class="add-btn add-btn-desktop" @click="showAddModal" title="新しい取引を追加">+</button>
      </div>
    </div>

    <!-- メニューのドロワー -->
    <div v-if="showMenu" class="side-menu-overlay" @click.self="toggleMenu">
      <div class="side-menu">
        <button class="menu-btn" @click="backupToCSV">CSVバックアップ</button>
        <button class="menu-btn" @click="showImportCSVModalMethod">CSVインポート</button>
        <button class="menu-btn" @click="openCreditCardSettings">クレジットカード設定</button>
        <button class="menu-btn" @click="showGraphModal">残高推移グラフ表示</button>
        <button class="menu-btn" @click="openTagChart">タグ別分析</button>
        <button class="menu-btn" @click="openSnapshotManager">スナップショット管理</button>
        <button v-if="!isWailsMode" class="menu-btn logout-btn" @click="logout">ログアウト</button>
      </div>
    </div>

    <!-- 残高表示と取引履歴を統合したカード -->
    <div class="card content-card">
      <div class="balance-section">
        <div class="balance-label">現在の残高</div>
        <div class="balance-amount">{{ formatCurrency(store.currentBalance) }}</div>
      </div>

      <div class="transaction-section">
        <table class="transaction-table">
          <thead>
            <tr>
              <th @click="toggleDateSort" style="cursor: pointer;">
                日付
                <span v-if="dateSortOrder === 'asc'">▲</span>
                <span v-if="dateSortOrder === 'desc'">▼</span>
              </th>
              <th v-if="store.shouldShowFundItemColumn">資金項目</th>
              <th>項目</th>
              <th>金額</th>
              <th>残高</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="transaction in sortedTransactions" :key="transaction.id" @click="onEditTransaction(transaction)">
              <td>{{ formatDateTime(transaction.date) }}</td>
              <td v-if="store.shouldShowFundItemColumn">{{ transaction.fundItem || transaction.account }}</td>
              <td>{{ transaction.item }}</td>
              <td :class="getAmountCellClass(transaction.type)">{{ formatAmount(transaction.amount, transaction.type) }}</td>
              <td>{{ isCreditCardItem(transaction.account || transaction.fundItem) ? '-' : formatCurrency(transaction.balance) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 新規取引追加モーダル -->
    <TransactionModal
      v-if="showAddTransactionModal"
      :is-edit-mode="isEditMode"
      :transaction="editingTransaction"
      :fund-items="store.accounts"
      :item-names="store.itemNames"
      @save="handleSaveTransaction"
      @delete="handleDeleteTransaction"
      @close="hideAddModal"
    />

    <!-- CSVインポートモーダル -->
    <CSVImportModal
      v-if="showImportCSVModal"
      @imported="handleCSVImported"
      @close="hideImportCSVModal"
    />

    <!-- クレジットカード設定モーダル -->
    <CreditCardSettingsModal
      v-if="showCreditCardModal"
      :fund-items="store.accounts"
      :selected-items="selectedCreditCardItems"
      @save="handleSaveCreditCardSettings"
      @close="hideCreditCardSettings"
    />

    <!-- 残高推移グラフモーダル -->
    <BalanceChart
      v-if="showGraph"
      :balance-history="balanceHistoryData"
      :credit-card-items="store.creditCardItems"
      @close="showGraph = false"
    />

    <!-- タグ別分析円グラフ (Agent.md §6.6) -->
    <TagPieChart
      v-if="showTagChart"
      :credit-card-items="store.creditCardItems"
      @close="showTagChart = false"
    />

    <!-- スナップショット管理モーダル -->
    <SnapshotManager
      v-if="showSnapshotModal"
      @close="showSnapshotModal = false"
      @restored="handleSnapshotRestored"
    />

    <!-- トースト通知 -->
    <Transition name="toast-fade">
      <div v-if="toast.visible" class="toast" :class="toast.type">
        {{ toast.message }}
      </div>
    </Transition>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useAppStore } from './store/index'
import TransactionModal from './components/TransactionModal.vue'
import CSVImportModal from './components/CSVImportModal.vue'
import CreditCardSettingsModal from './components/CreditCardSettingsModal.vue'
import BalanceChart from './components/BalanceChart.vue'
import SnapshotManager from './components/SnapshotManager.vue'
import TagPieChart from './components/TagPieChart.vue'
import {
  addTransaction,
  updateTransaction,
  deleteTransaction as apiDeleteTransaction,
  backupToCSVFile as apiBackupToCSVFile,
  saveCreditCardSettings as apiSaveCreditCardSettings,
  getBalanceHistoryFiltered,
  isWailsMode,
  logout as apiLogout
} from './utils/api'

const store = useAppStore()

// UI状態
const showMenu = ref(false)
const showAccountDropdown = ref(false)
const showAddTransactionModal = ref(false)
const showImportCSVModal = ref(false)
const showCreditCardModal = ref(false)
const showGraph = ref(false)
const showSnapshotModal = ref(false)
const showTagChart = ref(false)
const isEditMode = ref(false)
const editingTransaction = ref(null)
const dateSortOrder = ref('desc')
const selectedCreditCardItems = ref([])
const balanceHistoryData = ref(null)

// トースト通知
const toast = ref({ visible: false, message: '', type: 'success' })
let toastTimer = null
function showToast(message, type = 'success', duration = 3000) {
  clearTimeout(toastTimer)
  toast.value = { visible: true, message, type }
  toastTimer = setTimeout(() => {
    toast.value.visible = false
  }, duration)
}

// 日付でソートされた取引リスト
const sortedTransactions = computed(() => {
  const txs = [...store.transactions]
  txs.sort((a, b) => {
    const dateA = new Date(a.date)
    const dateB = new Date(b.date)
    const diff = dateSortOrder.value === 'asc' ? dateA - dateB : dateB - dateA
    if (diff !== 0) return diff
    return dateSortOrder.value === 'asc' ? a.id - b.id : b.id - a.id
  })
  return txs
})

// 通貨フォーマット
function formatCurrency(value) {
  if (value == null) return '¥0'
  return '¥' + value.toLocaleString('ja-JP')
}

function formatAmount(amount, type) {
  const prefix = type === 'income' ? '+' : '-'
  return prefix + '¥' + amount.toLocaleString('ja-JP')
}

function formatDateTime(dateStr) {
  if (!dateStr) return ''
  if (dateStr.includes(' ') && !dateStr.endsWith('00:00:00')) {
    return dateStr
  }
  return dateStr.split(' ')[0]
}

function getAmountCellClass(type) {
  return type === 'income' ? 'income-cell' : 'expense-cell'
}

function isCreditCardItem(account) {
  return store.creditCardItems.includes(account)
}

// メニュー操作
function toggleMenu() {
  showMenu.value = !showMenu.value
}

function toggleAccountDropdown() {
  showAccountDropdown.value = !showAccountDropdown.value
}

function toggleDateSort() {
  dateSortOrder.value = dateSortOrder.value === 'asc' ? 'desc' : 'asc'
}

// データ更新
async function refreshData() {
  await store.fetchTransactions()
}

let searchTimeout = null
function onSearchInput() {
  clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    store.fetchTransactions()
  }, 300)
}

// 取引モーダル操作
function showAddModal() {
  isEditMode.value = false
  editingTransaction.value = null
  showAddTransactionModal.value = true
  store.fetchItems()
}

function onEditTransaction(tx) {
  isEditMode.value = true
  editingTransaction.value = { ...tx }
  showAddTransactionModal.value = true
  store.fetchItems(tx.account || tx.fundItem)
}

function hideAddModal() {
  showAddTransactionModal.value = false
  editingTransaction.value = null
}

async function handleSaveTransaction(data) {
  try {
    if (isEditMode.value && editingTransaction.value) {
      await updateTransaction(editingTransaction.value.id, data)
    } else {
      await addTransaction(data)
    }
    hideAddModal()
    await store.fetchAccounts()
    await store.fetchTransactions()
  } catch (e) {
    console.error('取引保存エラー:', e)
    showToast('取引の保存に失敗しました: ' + e.message, 'error', 5000)
  }
}

async function handleDeleteTransaction() {
  if (!editingTransaction.value) return
  try {
    await apiDeleteTransaction(editingTransaction.value.id)
    hideAddModal()
    await store.fetchAccounts()
    await store.fetchTransactions()
  } catch (e) {
    console.error('取引削除エラー:', e)
    showToast('取引の削除に失敗しました: ' + e.message, 'error', 5000)
  }
}

// CSV関連
async function backupToCSV() {
  showMenu.value = false
  try {
    const filePath = await apiBackupToCSVFile()
    if (!filePath) {
      showToast('バックアップデータが空です', 'error')
      return
    }
    showToast('CSVバックアップを保存しました ✓')
  } catch (e) {
    console.error('CSVバックアップエラー:', e)
    showToast('CSVバックアップに失敗しました', 'error')
  }
}

function showImportCSVModalMethod() {
  showMenu.value = false
  showImportCSVModal.value = true
}

function hideImportCSVModal() {
  showImportCSVModal.value = false
}

async function handleCSVImported() {
  hideImportCSVModal()
  await store.fetchAccounts()
  await store.fetchTransactions()
}

// クレジットカード設定
async function openCreditCardSettings() {
  showMenu.value = false
  await store.fetchCreditCardSettings()
  selectedCreditCardItems.value = [...store.creditCardItems]
  showCreditCardModal.value = true
}

function hideCreditCardSettings() {
  showCreditCardModal.value = false
}

async function handleSaveCreditCardSettings(items) {
  try {
    await apiSaveCreditCardSettings(items)
    await store.fetchCreditCardSettings()
    hideCreditCardSettings()
    await store.fetchTransactions()
  } catch (e) {
    console.error('クレジットカード設定保存エラー:', e)
    showToast('クレジットカード設定の保存に失敗しました', 'error', 5000)
  }
}

// グラフモーダル
async function showGraphModal() {
  showMenu.value = false
  try {
    // クレジットカード除外済みの残高推移を取得
    const selectedAccounts = store.selectedFundItems.length > 0
      ? store.selectedFundItems
      : store.actualFundItems
    balanceHistoryData.value = await getBalanceHistoryFiltered(selectedAccounts)
    showGraph.value = true
  } catch (e) {
    console.error('残高推移取得エラー:', e)
    showToast('残高推移データの取得に失敗しました', 'error', 5000)
  }
}

// タグ別分析
function openTagChart() {
  showMenu.value = false
  showTagChart.value = true
}

// スナップショット管理
function openSnapshotManager() {
  showMenu.value = false
  showSnapshotModal.value = true
}

async function logout() {
  showMenu.value = false
  try {
    await apiLogout()
    window.location.href = '/login'
  } catch (e) {
    console.error('ログアウトエラー:', e)
    showToast('ログアウトに失敗しました', 'error', 5000)
  }
}

async function handleSnapshotRestored() {
  // 全状態をリセットしてから再取得
  store.resetState()
  try {
    await store.fetchAccounts()
    await store.fetchCreditCardSettings()
    await store.fetchTransactions()
    showToast('スナップショットから復元しました ✓')
  } catch (e) {
    console.error('復元後のデータ再取得エラー:', e)
    // 再取得に失敗した場合はページリロードで確実に反映
    window.location.reload()
  }
}

// グローバルクリックでドロップダウンを閉じる
function handleGlobalClick() {
  showAccountDropdown.value = false
}

// 初期化
onMounted(async () => {
  document.addEventListener('click', handleGlobalClick)
  await store.fetchAccounts()
  await store.fetchCreditCardSettings()
  await store.fetchTransactions()

  // スナップショット復元後のリロードならトースト通知を表示
  const restoreResult = localStorage.getItem('snapshot_restored')
  if (restoreResult) {
    localStorage.removeItem('snapshot_restored')
    if (restoreResult === 'success') {
      showToast('スナップショットから復元しました ✓')
    } else if (restoreResult.startsWith('error:')) {
      showToast('復元に失敗しました: ' + restoreResult.slice(6), 'error', 5000)
    }
  }
})
</script>
