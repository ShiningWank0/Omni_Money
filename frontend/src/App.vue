<template>
  <div id="app">
    <div v-if="idleScreenLocked" class="idle-lock-curtain" role="status" aria-live="polite">
      <div class="idle-lock-message">無操作タイムアウトのため画面をロックしました</div>
    </div>
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
        <button v-if="!isWailsMode" class="menu-btn" @click="openAIAPIConsole">AI API操作</button>
        <button class="menu-btn" @click="openBankAccountSettings">銀行口座設定</button>
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
      :credit-card-items="store.creditCardItems"
      :bank-account-items="store.bankAccountItems"
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

    <!-- AI専用API 管理コンソール（サーバーモードのみ） -->
    <AIAPIConsoleModal
      v-if="showAIAPIConsole"
      @close="showAIAPIConsole = false"
      @transaction-added="handleAITransactionAdded"
    />

    <!-- 銀行口座設定モーダル -->
    <CreditCardSettingsModal
      v-if="showBankAccountModal"
      title="銀行口座設定"
      item-label="銀行口座項目"
      dropdown-hint="カード引き落とし元として扱う資金項目を選択してください"
      :info-lines="bankAccountInfoLines"
      :fund-items="store.accounts"
      :selected-items="selectedBankAccountItems"
      @save="handleSaveBankAccountSettings"
      @close="hideBankAccountSettings"
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

    <!-- 最近の認証が必要な操作用の再認証ダイアログ -->
    <div v-if="showReauthModal" class="reauth-overlay" @click.self="cancelReauthentication">
      <div class="reauth-card" role="dialog" aria-modal="true" aria-labelledby="reauth-title">
        <h3 id="reauth-title">操作を続けるには再認証が必要です</h3>
        <p class="reauth-description">安全のため、もう一度Omni Moneyの認証情報を入力してください。</p>
        <form @submit.prevent="submitReauthentication">
          <label class="reauth-label" for="reauth-password">パスワード</label>
          <input
            id="reauth-password"
            ref="reauthPasswordInput"
            v-model="reauthPassword"
            class="reauth-input"
            type="password"
            autocomplete="current-password"
            maxlength="72"
            required
          >
          <template v-if="totpRequired">
            <label class="reauth-label" for="reauth-totp">認証アプリのコード</label>
            <input
              id="reauth-totp"
              v-model="reauthTotpCode"
              class="reauth-input"
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              pattern="[0-9]{6}"
              maxlength="6"
              required
            >
          </template>
          <div v-if="reauthError" class="reauth-error">{{ reauthError }}</div>
          <div class="reauth-actions">
            <button type="button" class="reauth-cancel" :disabled="reauthLoading" @click="cancelReauthentication">キャンセル</button>
            <button type="submit" class="reauth-submit" :disabled="reauthLoading">
              {{ reauthLoading ? '確認中...' : '認証して続行' }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- トースト通知 -->
    <Transition name="toast-fade">
      <div v-if="toast.visible" class="toast" :class="toast.type">
        {{ toast.message }}
      </div>
    </Transition>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useAppStore } from './store/index'
import TransactionModal from './components/TransactionModal.vue'
import CSVImportModal from './components/CSVImportModal.vue'
import CreditCardSettingsModal from './components/CreditCardSettingsModal.vue'
import BalanceChart from './components/BalanceChart.vue'
import SnapshotManager from './components/SnapshotManager.vue'
import TagPieChart from './components/TagPieChart.vue'
import AIAPIConsoleModal from './components/AIAPIConsoleModal.vue'
import {
  addTransaction,
  updateTransaction,
  deleteTransaction as apiDeleteTransaction,
  backupToCSVFile as apiBackupToCSVFile,
  saveCreditCardSettings as apiSaveCreditCardSettings,
  saveBankAccountSettings as apiSaveBankAccountSettings,
  getBalanceHistoryFiltered,
  isWailsMode,
  logout as apiLogout,
  getAuthStatus,
  reauthenticate
} from './utils/api'

const store = useAppStore()

// UI状態
const showMenu = ref(false)
const showAccountDropdown = ref(false)
const showAddTransactionModal = ref(false)
const showImportCSVModal = ref(false)
const showCreditCardModal = ref(false)
const showBankAccountModal = ref(false)
const showGraph = ref(false)
const showSnapshotModal = ref(false)
const showTagChart = ref(false)
const showAIAPIConsole = ref(false)
const showReauthModal = ref(false)
const reauthLoading = ref(false)
const reauthPassword = ref('')
const reauthTotpCode = ref('')
const totpRequired = ref(false)
const reauthError = ref('')
const reauthPasswordInput = ref(null)
let reauthRequest = null
let reauthListenerRegistered = false
const idleTimeoutSeconds = ref(0)
const idleScreenLocked = ref(false)
let idleTimer = null
let lastUserActivityAt = 0
let idleLockInProgress = false
let idleListenersRegistered = false
const isEditMode = ref(false)
const editingTransaction = ref(null)
const dateSortOrder = ref('desc')
const selectedCreditCardItems = ref([])
const selectedBankAccountItems = ref([])
const balanceHistoryData = ref(null)
const bankAccountInfoLines = [
  'カード支払い取引と銀行口座引き落とし取引の紐付け候補になります',
  '銀行口座項目は現在残高や残高推移の計算から除外されません',
  '紐付け機能はクレジットカード項目と銀行口座項目の組み合わせだけで使えます'
]

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

function openAIAPIConsole() {
  showMenu.value = false
  showAIAPIConsole.value = true
}

async function handleAITransactionAdded() {
  await store.fetchAccounts()
  await store.fetchTransactions()
  showToast('AI専用入口から取引を追加しました ✓')
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

// 銀行口座設定
async function openBankAccountSettings() {
  showMenu.value = false
  await store.fetchBankAccountSettings()
  selectedBankAccountItems.value = [...store.bankAccountItems]
  showBankAccountModal.value = true
}

function hideBankAccountSettings() {
  showBankAccountModal.value = false
}

async function handleSaveBankAccountSettings(items) {
  try {
    await apiSaveBankAccountSettings(items)
    await store.fetchBankAccountSettings()
    hideBankAccountSettings()
    await store.fetchTransactions()
  } catch (e) {
    console.error('銀行口座設定保存エラー:', e)
    showToast('銀行口座設定の保存に失敗しました', 'error', 5000)
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

function clearIdleTimer() {
  if (idleTimer !== null) {
    clearTimeout(idleTimer)
    idleTimer = null
  }
}

function stopIdleLock() {
  clearIdleTimer()
  if (!idleListenersRegistered) return
  document.removeEventListener('pointerdown', recordUserActivity)
  document.removeEventListener('keydown', recordUserActivity)
  document.removeEventListener('touchstart', recordUserActivity)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  idleListenersRegistered = false
}

// Drop every in-memory value that could reveal household financial data before
// navigating away. The v-if guards also unmount open modals and clear their
// component-local form state.
function clearSensitiveStateForIdle() {
  stopIdleLock()
  store.resetState()
  showMenu.value = false
  showAccountDropdown.value = false
  showAddTransactionModal.value = false
  showImportCSVModal.value = false
  showCreditCardModal.value = false
  showBankAccountModal.value = false
  showGraph.value = false
  showSnapshotModal.value = false
  showTagChart.value = false
  showAIAPIConsole.value = false
  showReauthModal.value = false
  reauthLoading.value = false
  reauthPassword.value = ''
  reauthTotpCode.value = ''
  reauthError.value = ''
  reauthPasswordInput.value = null
  isEditMode.value = false
  editingTransaction.value = null
  selectedCreditCardItems.value = []
  selectedBankAccountItems.value = []
  balanceHistoryData.value = null
  toast.value = { visible: false, message: '', type: 'success' }
  clearTimeout(toastTimer)
  toastTimer = null
  clearTimeout(searchTimeout)
  searchTimeout = null
  localStorage.removeItem('snapshot_restored')
  if (reauthRequest?.reject) {
    reauthRequest.reject(new Error('セッションが無操作タイムアウトになりました'))
  }
  reauthRequest = null
}

function checkIdleTimeout() {
  if (isWailsMode || idleLockInProgress || !idleTimeoutSeconds.value || !lastUserActivityAt) return
  if (Date.now() - lastUserActivityAt >= idleTimeoutSeconds.value * 1000) {
    void lockForIdle()
    return
  }
  scheduleIdleCheck()
}

function scheduleIdleCheck() {
  clearIdleTimer()
  if (isWailsMode || idleLockInProgress || !idleTimeoutSeconds.value || !lastUserActivityAt) return
  const remaining = idleTimeoutSeconds.value * 1000 - (Date.now() - lastUserActivityAt)
  idleTimer = setTimeout(checkIdleTimeout, Math.max(1, Math.min(remaining, 1000)))
}

function recordUserActivity() {
  if (isWailsMode || idleLockInProgress || document.visibilityState === 'hidden') return
  lastUserActivityAt = Date.now()
  scheduleIdleCheck()
}

function handleVisibilityChange() {
  if (!isWailsMode && document.visibilityState === 'visible') {
    checkIdleTimeout()
  }
}

async function lockForIdle() {
  if (isWailsMode || idleLockInProgress) return
  idleLockInProgress = true
  // Cover the entire UI before clearing stores or awaiting any network work.
  // This also prevents an already in-flight request from repainting sensitive
  // data during the short best-effort logout window.
  idleScreenLocked.value = true
  clearSensitiveStateForIdle()

  // Best effort: the browser may already be offline or the server may have
  // expired the session. The local purge and redirect remain mandatory.
  let timeoutID = null
  try {
    const timeout = new Promise(resolve => {
      timeoutID = setTimeout(resolve, 750)
    })
    await Promise.race([apiLogout(), timeout])
  } catch (error) {
    console.warn('アイドルタイムアウト時のログアウトに失敗しました:', error)
  } finally {
    if (timeoutID !== null) clearTimeout(timeoutID)
  }
  window.location.replace('/login?reason=idle')
}

function startIdleLock(seconds) {
  if (isWailsMode || !Number.isFinite(seconds) || seconds <= 0) return
  idleTimeoutSeconds.value = Math.floor(seconds)
  lastUserActivityAt = Date.now()
  document.addEventListener('pointerdown', recordUserActivity, { passive: true })
  document.addEventListener('keydown', recordUserActivity, { passive: true })
  document.addEventListener('touchstart', recordUserActivity, { passive: true })
  document.addEventListener('visibilitychange', handleVisibilityChange)
  idleListenersRegistered = true
  scheduleIdleCheck()
}

function handleReauthRequired(event) {
  // api.js coalesces simultaneous 428 responses, but ignore malformed or
  // duplicate events so a stale page cannot create an unresolvable promise.
  if (!event?.detail?.resolve || reauthRequest) return
  reauthRequest = event.detail
  reauthPassword.value = ''
  reauthTotpCode.value = ''
  reauthError.value = ''
  showReauthModal.value = true
  nextTick(() => reauthPasswordInput.value?.focus())
}

function cancelReauthentication() {
  if (reauthRequest?.reject) {
    reauthRequest.reject(new Error('再認証がキャンセルされました'))
  }
  reauthRequest = null
  showReauthModal.value = false
  reauthPassword.value = ''
  reauthTotpCode.value = ''
  reauthError.value = ''
}

async function submitReauthentication() {
  if (!reauthRequest || reauthLoading.value) return
  reauthLoading.value = true
  reauthError.value = ''
  try {
    await reauthenticate(reauthPassword.value, reauthTotpCode.value)
    reauthRequest.resolve(true)
    reauthRequest = null
    showReauthModal.value = false
    reauthPassword.value = ''
    reauthTotpCode.value = ''
  } catch (error) {
    reauthError.value = error?.message || '再認証に失敗しました'
    // Never retain credentials after a failed attempt.
    reauthPassword.value = ''
    reauthTotpCode.value = ''
    await nextTick()
    reauthPasswordInput.value?.focus()
  } finally {
    reauthLoading.value = false
  }
}

async function handleSnapshotRestored() {
  // 全状態をリセットしてから再取得
  store.resetState()
  try {
    await store.fetchAccounts()
    await store.fetchCreditCardSettings()
    await store.fetchBankAccountSettings()
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
  window.addEventListener('omni-money:reauth-required', handleReauthRequired)
  reauthListenerRegistered = true
  try {
    const authStatus = await getAuthStatus()
    // The same server flag controls both the login form and the recent-auth
    // dialog, so a TOTP field is never shown when TOTP is disabled.
    totpRequired.value = Boolean(authStatus?.totp_required)
    if (!isWailsMode && authStatus?.authenticated) {
      const serverIdleSeconds = Number(authStatus?.idle_timeout_seconds)
      if (Number.isFinite(serverIdleSeconds) && serverIdleSeconds > 0) {
        startIdleLock(serverIdleSeconds)
      }
    }
  } catch {
    // The normal API calls below provide the visible error/redirect behavior.
  }
  await store.fetchAccounts()
  await store.fetchCreditCardSettings()
  await store.fetchBankAccountSettings()
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

onBeforeUnmount(() => {
  document.removeEventListener('click', handleGlobalClick)
  stopIdleLock()
  if (reauthListenerRegistered) {
    window.removeEventListener('omni-money:reauth-required', handleReauthRequired)
  }
  if (reauthRequest?.reject) {
    reauthRequest.reject(new Error('再認証ダイアログが終了しました'))
  }
})
</script>

<style scoped>
.idle-lock-curtain {
  position: fixed;
  z-index: 100000;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 1.5rem;
  background: #f4f5f8;
}

.idle-lock-message {
  color: #333;
  font-weight: 600;
  text-align: center;
}

.reauth-overlay {
  position: fixed;
  z-index: 1000;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1rem;
  background: rgba(0, 0, 0, 0.45);
}

.reauth-card {
  width: min(420px, 100%);
  padding: 1.5rem;
  border-radius: 16px;
  background: #fff;
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.28);
}

.reauth-card h3 {
  margin: 0 0 0.6rem;
  color: #333;
}

.reauth-description {
  margin: 0 0 1.25rem;
  color: #666;
  font-size: 0.9rem;
}

.reauth-label {
  display: block;
  margin: 0.75rem 0 0.35rem;
  color: #333;
  font-size: 0.9rem;
  font-weight: 600;
}

.reauth-input {
  width: 100%;
  box-sizing: border-box;
  padding: 0.7rem 0.8rem;
  border: 2px solid rgba(102, 126, 234, 0.2);
  border-radius: 9px;
}

.reauth-input:focus {
  outline: none;
  border-color: #667eea;
  box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.15);
}

.reauth-error {
  margin-top: 0.8rem;
  padding: 0.6rem 0.75rem;
  border-radius: 8px;
  background: rgba(255, 69, 58, 0.1);
  color: #b00020;
  font-size: 0.85rem;
}

.reauth-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
  margin-top: 1.25rem;
}

.reauth-actions button {
  padding: 0.65rem 0.9rem;
  border: 0;
  border-radius: 8px;
  cursor: pointer;
  font-weight: 600;
}

.reauth-actions button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.reauth-cancel {
  background: #eee;
  color: #333;
}

.reauth-submit {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: #fff;
}
</style>
