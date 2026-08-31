<template>
  <div id="app">
    <DesktopVaultGate
      v-if="isWailsMode && !desktopVaultUnlocked"
      :status="desktopVaultStatus"
      :loading="desktopVaultLoading"
      :fatal-error="desktopVaultError"
      @unlocked="handleDesktopVaultUnlocked"
    />
    <template v-else>
    <div v-if="idleScreenLocked" class="idle-lock-curtain" role="status" aria-live="polite">
      <div class="idle-lock-message">無操作タイムアウトのため画面をロックしました</div>
    </div>
    <!-- ヘッダーエリア -->
    <div class="card header">
      <div class="header-top">
        <div class="header-left">
          <button
            type="button"
            class="hamburger-menu"
            :class="{ 'menu-open': showMenu }"
            :aria-label="showMenu ? 'メニューを閉じる' : 'メニューを開く'"
            :aria-expanded="showMenu"
            aria-controls="side-menu"
            @click="toggleMenu"
          >
            <span class="menu-glyph" aria-hidden="true"></span>
          </button>
          <div class="project-selector" @click.stop>
            <button
              type="button"
              class="project-selector-button"
              :aria-expanded="showAccountDropdown"
              aria-controls="account-dropdown"
              aria-haspopup="true"
              @click="toggleAccountDropdown"
            >
              <span class="chevron-anim" aria-hidden="true">
                <span v-if="showAccountDropdown" key="down">▼</span>
                <span v-else key="up">▶</span>
              </span>
              <span>{{ store.selectedFundItemDisplay }}</span>
            </button>
            <div v-if="showAccountDropdown" id="account-dropdown" class="account-dropdown" @click.stop>
              <div class="fund-item-header">
                <button type="button" @click="store.toggleAllFundItems(); refreshData()" class="toggle-all-btn">
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
          <button type="button" class="add-btn" @click="showAddModal" title="新しい取引を追加" aria-label="新しい取引を追加">+</button>
        </div>
      </div>
      <div class="header-search">
        <div class="search-container">
          <label for="transaction-search" class="sr-only">取引を項目名またはメモで検索</label>
          <input id="transaction-search" type="search" class="search-box" placeholder="項目名・メモで検索" v-model="store.searchQuery" @input="onSearchInput">
          <span class="search-icon" aria-hidden="true">🔍</span>
        </div>
        <button type="button" class="add-btn add-btn-desktop" @click="showAddModal" title="新しい取引を追加" aria-label="新しい取引を追加">+</button>
      </div>
    </div>

    <!-- メニューのドロワー -->
    <div v-if="showMenu" class="side-menu-overlay" @click.self="toggleMenu">
      <nav id="side-menu" class="side-menu" aria-label="アプリケーションメニュー">
        <button class="menu-btn" @click="backupToCSV">CSVバックアップ</button>
        <button class="menu-btn" @click="showImportCSVModalMethod">CSVインポート</button>
        <button class="menu-btn menu-group-start" @click="openCreditCardSettings">クレジットカード設定</button>
        <button v-if="serverFeatures.ai" class="menu-btn" @click="openAIAPIConsole">AI API操作</button>
        <button class="menu-btn" @click="openBankAccountSettings">銀行口座設定</button>
        <button class="menu-btn menu-group-start" @click="showGraphModal">残高推移グラフ表示</button>
        <button class="menu-btn" @click="openTagChart">タグ別分析</button>
        <button class="menu-btn" @click="openTagManager">タグ管理</button>
        <button v-if="serverFeatures.snapshots" class="menu-btn menu-group-start" @click="openSnapshotManager">スナップショット管理</button>
        <button v-if="!isWailsMode && serverFeatures.passkeys" class="menu-btn menu-group-start" @click="openPasskeySettings">パスキー設定</button>
        <button v-if="serverFeatures.admin" class="menu-btn menu-group-start" @click="openServerAccountAdmin">サーバーユーザー管理</button>
        <button v-if="isWailsMode" class="menu-btn logout-btn" @click="lockDesktopVaultNow">保管庫をロック</button>
        <button v-if="!isWailsMode" class="menu-btn logout-btn" @click="logout">ログアウト</button>
      </nav>
    </div>

    <!-- 残高表示と取引履歴を統合したカード -->
    <div class="card content-card">
      <div class="balance-section">
        <div class="balance-label">現在の残高</div>
        <div class="balance-amount" aria-live="polite">{{ formatCurrency(store.currentBalance) }}</div>
      </div>

      <div class="transaction-section" :aria-busy="isTableLoading">
        <table class="transaction-table">
          <caption class="sr-only">取引履歴</caption>
          <thead>
            <tr>
              <th scope="col" :aria-sort="dateSortOrder === 'asc' ? 'ascending' : 'descending'">
                <button type="button" class="sort-button" @click="toggleDateSort">
                  日付
                  <span aria-hidden="true">{{ dateSortOrder === 'asc' ? '▲' : '▼' }}</span>
                </button>
              </th>
              <th v-if="store.shouldShowFundItemColumn" scope="col">資金項目</th>
              <th scope="col">項目</th>
              <th scope="col" class="numeric-column">金額</th>
              <th scope="col" class="numeric-column">残高</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="isTableLoading" class="table-status-row">
              <td :colspan="transactionColumnCount"><span role="status">取引を読み込んでいます…</span></td>
            </tr>
            <tr v-else-if="sortedTransactions.length === 0" class="table-status-row">
              <td :colspan="transactionColumnCount">{{ store.searchQuery ? '検索条件に一致する取引はありません' : '取引はまだありません' }}</td>
            </tr>
            <template v-else>
              <tr
                v-for="transaction in sortedTransactions"
                :key="transaction.id"
                tabindex="0"
                :aria-label="getTransactionAriaLabel(transaction)"
                @click="onEditTransaction(transaction)"
                @keydown.enter="onEditTransaction(transaction)"
                @keydown.space.prevent="onEditTransaction(transaction)"
              >
                <td class="date-cell">{{ formatDateTime(transaction.date) }}</td>
                <td v-if="store.shouldShowFundItemColumn">{{ transaction.fundItem || transaction.account }}</td>
                <td>{{ transaction.item }}</td>
                <td class="numeric-cell" :class="getAmountCellClass(transaction.type)">{{ formatAmount(transaction.amount, transaction.type, transaction.amount_exact) }}</td>
                <td class="numeric-cell">{{ isCreditCardItem(transaction.account || transaction.fundItem) ? '-' : formatCurrency(transaction.balance, transaction.balance_exact) }}</td>
              </tr>
            </template>
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

    <TagManager
      v-if="showTagManager"
      :tags="tagManagerTags"
      @changed="tagManagerTags = $event"
      @close="showTagManager = false"
    />

    <!-- スナップショット管理モーダル -->
    <SnapshotManager
      v-if="showSnapshotModal"
      @close="showSnapshotModal = false"
      @restored="handleSnapshotRestored"
    />

    <ServerAccountAdminModal
      v-if="showServerAccountAdmin"
      :current-user-id="currentServerUserId"
      @close="showServerAccountAdmin = false"
    />

    <PasskeySettingsModal
      v-if="showPasskeySettings"
      @close="showPasskeySettings = false"
    />

    <!-- 最近の認証が必要な操作用の再認証ダイアログ -->
    <div v-if="showReauthModal" class="reauth-overlay" @click.self="cancelReauthentication">
      <div class="reauth-card" role="dialog" aria-modal="true" aria-labelledby="reauth-title">
        <h3 id="reauth-title">重要な操作の確認</h3>
        <p class="reauth-description">ユーザー管理、一括取り込み・書き出し、復元など重要な操作を実行するため、パスワードまたは登録済みパスキーで確認してください。</p>
        <form @submit.prevent="submitReauthentication">
          <label class="reauth-label" for="reauth-password">パスワード</label>
          <input
            id="reauth-password"
            ref="reauthPasswordInput"
            v-model="reauthPassword"
            class="reauth-input"
            type="password"
            autocomplete="current-password"
            maxlength="256"
            required
          >
          <div v-if="reauthError" class="reauth-error">{{ reauthError }}</div>
          <div class="reauth-actions">
            <button type="button" class="reauth-cancel" :disabled="reauthLoading" @click="cancelReauthentication">キャンセル</button>
            <button type="submit" class="reauth-submit" :disabled="reauthLoading">
              {{ reauthLoading ? '確認中...' : '確認して実行' }}
            </button>
          </div>
          <template v-if="canUsePasskeyReauth">
            <div class="reauth-divider"><span>または</span></div>
            <button type="button" class="reauth-passkey" :disabled="reauthLoading" @click="submitPasskeyReauthentication">
              {{ reauthLoading ? '確認中...' : 'パスキーで確認' }}
            </button>
          </template>
        </form>
      </div>
    </div>

    <!-- トースト通知 -->
    <Transition name="toast-fade">
      <div
        v-if="toast.visible"
        class="toast"
        :class="toast.type"
        :role="toast.type === 'error' ? 'alert' : 'status'"
        :aria-live="toast.type === 'error' ? 'assertive' : 'polite'"
      >
        {{ toast.message }}
      </div>
    </Transition>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, defineAsyncComponent, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useAppStore } from './store/index'
import TransactionModal from './components/TransactionModal.vue'
import DesktopVaultGate from './components/DesktopVaultGate.vue'
import { csvExportWarning } from './utils/csvSafety'
import { isDesktopVaultUnlocked } from './utils/desktopVaultSafety'
import { passkeysSupported } from './utils/passkeys'
import { formatExactCurrency } from './utils/exactAmount'

// 初期表示に不要な管理・分析モーダルは、開いた時だけ読み込む。
const CSVImportModal = defineAsyncComponent(() => import('./components/CSVImportModal.vue'))
const CreditCardSettingsModal = defineAsyncComponent(() => import('./components/CreditCardSettingsModal.vue'))
const BalanceChart = defineAsyncComponent(() => import('./components/BalanceChart.vue'))
const SnapshotManager = defineAsyncComponent(() => import('./components/SnapshotManager.vue'))
const TagPieChart = defineAsyncComponent(() => import('./components/TagPieChart.vue'))
const TagManager = defineAsyncComponent(() => import('./components/TagManager.vue'))
const AIAPIConsoleModal = defineAsyncComponent(() => import('./components/AIAPIConsoleModal.vue'))
const ServerAccountAdminModal = defineAsyncComponent(() => import('./components/ServerAccountAdminModal.vue'))
const PasskeySettingsModal = defineAsyncComponent(() => import('./components/PasskeySettingsModal.vue'))
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
  getDesktopVaultStatus,
  lockDesktopVault,
  reauthenticate,
  reauthenticateWithPasskey,
  keepAlive,
  getTags
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
const showTagManager = ref(false)
const tagManagerTags = ref([])
const showAIAPIConsole = ref(false)
const showServerAccountAdmin = ref(false)
const showPasskeySettings = ref(false)
const showReauthModal = ref(false)
const reauthLoading = ref(false)
const reauthPassword = ref('')
const reauthError = ref('')
const reauthPasswordInput = ref(null)
let reauthRequest = null
let reauthListenerRegistered = false
const idleTimeoutSeconds = ref(0)
const serverFeatures = ref({ admin: false, ai: false, snapshots: isWailsMode, passkeys: false })
const canUsePasskeyReauth = computed(() => !isWailsMode && serverFeatures.value.passkeys && passkeysSupported())
const currentServerUserId = ref('')
const idleScreenLocked = ref(false)
const desktopVaultStatus = ref(null)
const desktopVaultLoading = ref(isWailsMode)
const desktopVaultError = ref('')
const desktopVaultUnlocked = computed(() => !isWailsMode || isDesktopVaultUnlocked(desktopVaultStatus.value))
let idleTimer = null
let lastUserActivityAt = 0
let idleLockInProgress = false
let idleListenersRegistered = false
let activityThrottleTimer = null
let heartbeatIntervalTimer = null
let heartbeatTrailingTimer = null
let heartbeatInFlight = false
let heartbeatPending = false
let heartbeatRecheckInFlight = false
let lastHeartbeatAttemptAt = 0
let componentMounted = false
const heartbeatIntervalMs = 4 * 60 * 1000
const heartbeatTrailingMs = 30 * 1000
const heartbeatFailureRecheckDelaysMs = [500, 1500]
const isEditMode = ref(false)
const editingTransaction = ref(null)
const dateSortOrder = ref('desc')
const isInitialLoading = ref(true)
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

const isTableLoading = computed(() => isInitialLoading.value || store.loading)
const transactionColumnCount = computed(() => store.shouldShowFundItemColumn ? 5 : 4)

// 通貨フォーマット
function formatCurrency(value, exactValue) {
  return formatExactCurrency(value, exactValue)
}

function formatAmount(amount, type, amountExact) {
  const prefix = type === 'income' ? '+' : '-'
  return prefix + formatExactCurrency(amount, amountExact)
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

function getTransactionAriaLabel(transaction) {
  const account = transaction.fundItem || transaction.account || '資金項目なし'
  return `${formatDateTime(transaction.date)}、${account}、${transaction.item}、${formatAmount(transaction.amount, transaction.type, transaction.amount_exact)}。編集する`
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

  const confirmed = window.confirm(csvExportWarning(isWailsMode))
  if (!confirmed) return

  try {
    const filePath = await apiBackupToCSVFile()
    if (!filePath) {
      if (isWailsMode) showToast('CSV保存をキャンセルしました')
      else showToast('CSVバックアップを作成できませんでした', 'error')
      return
    }
    showToast(isWailsMode
      ? 'CSVバックアップを保存しました ✓'
      : 'CSVのダウンロードを開始しました。暗号化済みの保存先を確認してください ✓')
  } catch (e) {
    if (e?.message?.includes('キャンセル')) {
      showToast('CSV保存をキャンセルしました')
      return
    }
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

async function openTagManager() {
  showMenu.value = false
  try {
    tagManagerTags.value = await getTags()
    showTagManager.value = true
  } catch (error) {
    showToast(error?.message || 'タグ一覧の取得に失敗しました', 'error', 5000)
  }
}

// スナップショット管理
function openSnapshotManager() {
  showMenu.value = false
  showSnapshotModal.value = true
}

function openServerAccountAdmin() {
  showMenu.value = false
  showServerAccountAdmin.value = true
}

function openPasskeySettings() {
  showMenu.value = false
  showPasskeySettings.value = true
}

async function logout() {
  showMenu.value = false
  try {
    await apiLogout()
  } catch (e) {
    console.error('ログアウトエラー:', e)
  } finally {
    clearSensitiveStateForIdle()
    window.location.replace('/login')
  }
}

function clearIdleTimer() {
  if (idleTimer !== null) {
    clearTimeout(idleTimer)
    idleTimer = null
  }
}

function clearHeartbeatTimers() {
  if (heartbeatIntervalTimer !== null) {
    clearTimeout(heartbeatIntervalTimer)
    heartbeatIntervalTimer = null
  }
  if (heartbeatTrailingTimer !== null) {
    clearTimeout(heartbeatTrailingTimer)
    heartbeatTrailingTimer = null
  }
  heartbeatPending = false
}

function stopIdleLock() {
  clearIdleTimer()
  clearHeartbeatTimers()
  if (activityThrottleTimer !== null) {
    clearTimeout(activityThrottleTimer)
    activityThrottleTimer = null
  }
  if (!idleListenersRegistered) return
  document.removeEventListener('pointerdown', recordUserActivity)
  document.removeEventListener('keydown', recordUserActivity)
  document.removeEventListener('touchstart', recordUserActivity)
  document.removeEventListener('pointermove', recordThrottledUserActivity)
  document.removeEventListener('wheel', recordThrottledUserActivity)
  window.removeEventListener('scroll', recordThrottledUserActivity)
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
  showTagManager.value = false
  tagManagerTags.value = []
  showAIAPIConsole.value = false
  showServerAccountAdmin.value = false
  showPasskeySettings.value = false
  showReauthModal.value = false
  reauthLoading.value = false
  reauthPassword.value = ''
  reauthError.value = ''
  reauthPasswordInput.value = null
  isEditMode.value = false
  editingTransaction.value = null
  selectedCreditCardItems.value = []
  selectedBankAccountItems.value = []
  balanceHistoryData.value = null
  currentServerUserId.value = ''
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

async function fetchPrivateData() {
  await store.fetchAccounts()
  await store.fetchCreditCardSettings()
  await store.fetchBankAccountSettings()
  await store.fetchTransactions()
}

async function handleDesktopVaultUnlocked() {
  if (!isWailsMode) return
  desktopVaultLoading.value = true
  desktopVaultError.value = ''
  try {
    const confirmed = await getDesktopVaultStatus()
    if (!isDesktopVaultUnlocked(confirmed)) {
      throw new Error('保管庫のロック解除を確認できませんでした')
    }
    desktopVaultStatus.value = confirmed
    await fetchPrivateData()
    isInitialLoading.value = false
  } catch (error) {
    clearSensitiveStateForIdle()
    desktopVaultStatus.value = { state: 'locked', configured: true, unlocked: false }
    desktopVaultError.value = error?.message || '保管庫を安全に開けませんでした'
  } finally {
    desktopVaultLoading.value = false
  }
}

async function lockDesktopVaultNow() {
  if (!isWailsMode) return
  showMenu.value = false
  isInitialLoading.value = true
  desktopVaultError.value = ''
  desktopVaultStatus.value = { ...(desktopVaultStatus.value || {}), state: 'locked', configured: true, unlocked: false }
  clearSensitiveStateForIdle()
  // Let Vue replace the ledger with the lock gate before waiting for DB close.
  await nextTick()
  try {
    await lockDesktopVault()
    desktopVaultStatus.value = await getDesktopVaultStatus()
  } catch (error) {
    desktopVaultError.value = error?.message || '保管庫を安全にロックできませんでした'
  }
}

function applyServerAuthStatus(status) {
  if (isWailsMode || !status?.features) return
  serverFeatures.value = {
    admin: Boolean(status.features.admin),
    ai: Boolean(status.features.ai),
    snapshots: Boolean(status.features.snapshots),
    passkeys: Boolean(status.features.passkeys)
  }
  currentServerUserId.value = typeof status?.user?.id === 'string' ? status.user.id : ''
}

async function handlePageShow(event) {
  if (isWailsMode || !event.persisted) return

  isInitialLoading.value = true
  clearSensitiveStateForIdle()
  try {
    const authStatus = await getAuthStatus()
    if (componentMounted) applyServerAuthStatus(authStatus)
    if (!authStatus?.authenticated) {
      window.location.replace('/login')
      return
    }
    const serverIdleSeconds = Number(authStatus?.idle_timeout_seconds)
    if (Number.isFinite(serverIdleSeconds) && serverIdleSeconds > 0) {
      startIdleLock(serverIdleSeconds)
    }
    await fetchPrivateData()
    isInitialLoading.value = false
  } catch (e) {
    console.error('キャッシュ復元後の再認証エラー:', e)
    window.location.replace('/login')
  }
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
  scheduleActivityHeartbeat()
}

// The server only refreshes LastSeenAt when it receives an authenticated
// request. Keep the client-side activity lock and server idle timeout aligned
// with a low-frequency, visible-tab heartbeat. A trailing request covers a
// user who stops scrolling or moving the pointer before the four-minute
// periodic interval; no heartbeat is emitted without real user activity.
async function sendActivityHeartbeat() {
  if (!componentMounted || isWailsMode || idleLockInProgress || document.visibilityState === 'hidden') return
  if (heartbeatInFlight) {
    heartbeatPending = true
    return
  }

  heartbeatInFlight = true
  if (heartbeatIntervalTimer !== null) {
    clearTimeout(heartbeatIntervalTimer)
    heartbeatIntervalTimer = null
  }
  // Cadence is based on attempts, not only successful responses. A failed
  // request must not produce a zero-delay retry loop while the user keeps
  // moving the pointer or scrolling.
  lastHeartbeatAttemptAt = Date.now()
  try {
    const status = await keepAlive()
    // A cross-tab rotation can produce 403 (new shared cookie, old in-memory
    // CSRF token), while a request that left just before rotation can produce
    // 401. Status rechecking both cases either refreshes the current CSRF token
    // or confirms that a full login is genuinely required.
    if (status === 401 || status === 403) void recheckSessionAfterHeartbeatFailure()
  } catch {
    // Heartbeats are best effort. A network failure is inconclusive and must
    // not redirect or erase the screen; authentication/CSRF failures are
    // rechecked separately so a rotation is not mistaken for expiry.
  } finally {
    heartbeatInFlight = false
    if (heartbeatPending) {
      heartbeatPending = false
      if (componentMounted && !idleLockInProgress && document.visibilityState !== 'hidden') {
        // A pending event only records that activity happened during the
        // request. Respect the same four-minute cadence before retrying.
        scheduleHeartbeatInterval()
      }
    } else if (componentMounted && !idleLockInProgress && document.visibilityState !== 'hidden' &&
               Date.now() - lastUserActivityAt <= heartbeatTrailingMs) {
      scheduleHeartbeatInterval()
    }
  }
}

function scheduleHeartbeatInterval() {
  if (heartbeatIntervalTimer !== null || !componentMounted || isWailsMode || idleLockInProgress ||
      document.visibilityState === 'hidden' || !lastUserActivityAt) return
  const elapsed = Date.now() - lastHeartbeatAttemptAt
  const delay = Math.max(0, heartbeatIntervalMs - elapsed)
  heartbeatIntervalTimer = setTimeout(() => {
    heartbeatIntervalTimer = null
    // If activity stopped, the trailing timer is sufficient and a periodic
    // timer must not keep an unattended session alive.
    if (componentMounted && Date.now() - lastUserActivityAt <= heartbeatTrailingMs) {
      void sendActivityHeartbeat()
    }
  }, delay)
}

async function recheckSessionAfterHeartbeatFailure() {
  if (heartbeatRecheckInFlight) return
  heartbeatRecheckInFlight = true
  let confirmedExpired = false
  try {
    // A heartbeat can race with a same-tab or cross-tab session rotation: the
    // server has invalidated the old cookie, but the browser may not have
    // received Set-Cookie yet. Two spaced status checks avoid turning that
    // short race (or Pangolin latency) into a surprising full-login redirect.
    for (const delay of heartbeatFailureRecheckDelaysMs) {
      await new Promise(resolve => setTimeout(resolve, delay))
      if (!componentMounted || isWailsMode || idleLockInProgress || document.visibilityState === 'hidden') return
      // The explicit reauthentication request owns expiry handling while it
      // is in flight. Never carry a stale unauthenticated probe across the
      // session rotation and apply it after the new cookie arrives.
      if (reauthLoading.value) return
      try {
        const status = await getAuthStatus()
        if (status?.authenticated !== false) return
        confirmedExpired = true
      } catch {
        // A network failure is inconclusive. Do not erase the screen; the next
        // real user activity can perform another bounded heartbeat check.
        return
      }
    }
    if (confirmedExpired && !reauthLoading.value && !idleLockInProgress &&
        document.visibilityState !== 'hidden') {
      expireSessionAndRedirect('session-expired')
    }
  } finally {
    heartbeatRecheckInFlight = false
  }
}

function expireSessionAndRedirect(reason) {
  if (!componentMounted || isWailsMode || idleLockInProgress) return
  idleLockInProgress = true
  idleScreenLocked.value = true
  clearSensitiveStateForIdle()
  window.location.replace(`/login?reason=${encodeURIComponent(reason)}`)
}

function scheduleActivityHeartbeat() {
  if (!componentMounted || isWailsMode || idleLockInProgress || document.visibilityState === 'hidden') return
  scheduleHeartbeatInterval()
  if (heartbeatTrailingTimer !== null) clearTimeout(heartbeatTrailingTimer)
  heartbeatTrailingTimer = setTimeout(() => {
    heartbeatTrailingTimer = null
    if (componentMounted && !idleLockInProgress && document.visibilityState !== 'hidden') {
      void sendActivityHeartbeat()
    }
  }, heartbeatTrailingMs)
}

// Pointer movement and scrolling can fire hundreds of events per second. Treat
// them as activity at most once per second, while keeping explicit clicks,
// keypresses, and touch starts immediate. The visibility guard in
// recordUserActivity prevents background tabs from extending the idle window.
function recordThrottledUserActivity() {
  if (activityThrottleTimer !== null) return
  recordUserActivity()
  activityThrottleTimer = setTimeout(() => {
    activityThrottleTimer = null
  }, 1000)
}

function handleVisibilityChange() {
  if (isWailsMode) return
  if (document.visibilityState === 'hidden') {
    // Do not let a background tab keep either the local or server session
    // alive. Any pending heartbeat is discarded and a later visible-tab
    // activity will schedule a fresh one.
    clearHeartbeatTimers()
    return
  }
  checkIdleTimeout()
}

async function lockForIdle() {
  if (!componentMounted || isWailsMode || idleLockInProgress) return
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
  // getAuthStatus() immediately before this call has touched the server.
  lastHeartbeatAttemptAt = lastUserActivityAt
  document.addEventListener('pointerdown', recordUserActivity, { passive: true })
  document.addEventListener('keydown', recordUserActivity, { passive: true })
  document.addEventListener('touchstart', recordUserActivity, { passive: true })
  document.addEventListener('pointermove', recordThrottledUserActivity, { passive: true })
  document.addEventListener('wheel', recordThrottledUserActivity, { passive: true })
  window.addEventListener('scroll', recordThrottledUserActivity, { passive: true })
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
  reauthError.value = ''
  showReauthModal.value = true
  nextTick(() => reauthPasswordInput.value?.focus())
}

function handleSessionExpired(event) {
  event?.preventDefault?.()
  expireSessionAndRedirect(event?.detail?.reason || 'session-expired')
}

function cancelReauthentication() {
  if (reauthRequest?.reject) {
    reauthRequest.reject(new Error('再認証がキャンセルされました'))
  }
  reauthRequest = null
  showReauthModal.value = false
  reauthPassword.value = ''
  reauthError.value = ''
}

async function submitReauthentication() {
  if (!reauthRequest || reauthLoading.value) return
  reauthLoading.value = true
  reauthError.value = ''
  try {
    await reauthenticate(reauthPassword.value)
    reauthRequest.resolve(true)
    reauthRequest = null
    showReauthModal.value = false
    reauthPassword.value = ''
  } catch (error) {
    reauthError.value = error?.message || '再認証に失敗しました'
    // Never retain credentials after a failed attempt.
    reauthPassword.value = ''
    await nextTick()
    reauthPasswordInput.value?.focus()
  } finally {
    reauthLoading.value = false
  }
}

async function submitPasskeyReauthentication() {
  if (!reauthRequest || reauthLoading.value) return
  reauthLoading.value = true
  reauthError.value = ''
  reauthPassword.value = ''
  try {
    await reauthenticateWithPasskey()
    reauthRequest.resolve(true)
    reauthRequest = null
    showReauthModal.value = false
  } catch (error) {
    reauthError.value = error?.message || 'パスキー再認証に失敗しました'
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
  componentMounted = true
  document.addEventListener('click', handleGlobalClick)
  window.addEventListener('omni-money:reauth-required', handleReauthRequired)
  window.addEventListener('omni-money:session-expired', handleSessionExpired)
  window.addEventListener('pageshow', handlePageShow)
  reauthListenerRegistered = true
  if (isWailsMode) {
    try {
      desktopVaultStatus.value = await getDesktopVaultStatus()
      if (desktopVaultUnlocked.value) {
        await fetchPrivateData()
      }
    } catch (error) {
      clearSensitiveStateForIdle()
      desktopVaultError.value = error?.message || '保管庫の状態を安全に確認できませんでした'
      desktopVaultStatus.value = { state: 'error', configured: true, unlocked: false }
    } finally {
      desktopVaultLoading.value = false
      isInitialLoading.value = false
    }
    return
  }
  try {
    const authStatus = await getAuthStatus()
    if (componentMounted) applyServerAuthStatus(authStatus)
    if (componentMounted && !isWailsMode && authStatus?.authenticated) {
      const serverIdleSeconds = Number(authStatus?.idle_timeout_seconds)
      if (Number.isFinite(serverIdleSeconds) && serverIdleSeconds > 0) {
        startIdleLock(serverIdleSeconds)
      }
    }
  } catch {
    // The normal API calls below provide the visible error/redirect behavior.
  }
  await fetchPrivateData()
  isInitialLoading.value = false

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
  componentMounted = false
  document.removeEventListener('click', handleGlobalClick)
  window.removeEventListener('pageshow', handlePageShow)
  clearTimeout(searchTimeout)
  clearTimeout(toastTimer)
  stopIdleLock()
  if (reauthListenerRegistered) {
    window.removeEventListener('omni-money:reauth-required', handleReauthRequired)
    window.removeEventListener('omni-money:session-expired', handleSessionExpired)
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
  z-index: 2000;
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
.reauth-divider { display: flex; align-items: center; gap: 0.75rem; margin: 1rem 0; color: #777; font-size: 0.8rem; }
.reauth-divider::before, .reauth-divider::after { content: ''; flex: 1; height: 1px; background: #dde1f6; }
.reauth-passkey { width: 100%; padding: 0.65rem; border: 1px solid #667eea; border-radius: 8px; color: #4d5fc7; background: #fff; cursor: pointer; }
.reauth-passkey:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
