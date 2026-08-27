// Wailsバインディングへのラッパー関数
// デスクトップモード時はWailsのGoバインディングを直接呼び出し、
// サーバーモード時はREST APIを呼び出すよう抽象化する

export const isWailsMode = typeof window.go !== 'undefined'
const isWails = isWailsMode

// CSRF tokens are deliberately kept in memory only.  They are issued by the
// server after authentication and are never persisted in localStorage or a
// cookie that JavaScript can read.
let csrfToken = null
let pendingReauthentication = null

function rememberAuthToken(data) {
  if (typeof data?.csrf_token === 'string' && data.csrf_token.length > 0) {
    csrfToken = data.csrf_token
  } else if (data?.authenticated === false) {
    csrfToken = null
  }
}

function isUnsafeMethod(method) {
  return !['GET', 'HEAD', 'OPTIONS'].includes(method)
}

function requestReauthentication() {
  if (pendingReauthentication) return pendingReauthentication

  pendingReauthentication = new Promise((resolve, reject) => {
    // App.vue owns the modal and supplies credentials only when the user
    // explicitly confirms re-authentication.  Keeping this bridge in the
    // API layer lets any sensitive request (including future ones) resume
    // without every component implementing its own retry logic.
    window.dispatchEvent(new CustomEvent('omni-money:reauth-required', {
      detail: { resolve, reject }
    }))
  }).finally(() => {
    pendingReauthentication = null
  })

  return pendingReauthentication
}

function getPathname(url) {
  try {
    return new URL(url, window.location.origin).pathname
  } catch {
    return ''
  }
}

export async function apiFetch(url, options = {}, config = {}) {
  const { skipAuthRedirect = false, skipReauth = false } = config
  const method = (options.method || 'GET').toUpperCase()
  const headers = new Headers(options.headers || {})
  if (isUnsafeMethod(method) && csrfToken) {
    // Never let a caller accidentally replay a token that was rotated by a
    // successful re-authentication.
    headers.set('X-CSRF-Token', csrfToken)
  }

  const response = await fetch(url, {
    credentials: 'include',
    ...options,
    cache: 'no-store',
    headers
  })

  const path = getPathname(url)

  if (!isWailsMode && !skipReauth && response.status === 428 &&
      !['/api/auth/login', '/api/auth/reauth', '/api/auth/status'].includes(path)) {
    await requestReauthentication()
    // The original request body is retained in `options` (all current API
    // callers use replayable strings).  Retry exactly once after fresh auth.
    return await apiFetch(url, options, { ...config, skipReauth: true })
  }

  if (!isWailsMode && !skipAuthRedirect && response.status === 401) {
    const skipPaths = new Set(['/api/auth/login', '/api/auth/status'])
    if (!skipPaths.has(path) && window.location.pathname !== '/login') {
      window.location.replace('/login')
    }
    throw new Error('認証が必要です')
  }

  return response
}

async function parseError(response, fallbackMessage) {
  try {
    const data = await response.json()
    return data?.error || fallbackMessage
  } catch {
    return fallbackMessage
  }
}

async function throwIfNotOk(response, fallbackMessage) {
  if (!response.ok) {
    throw new Error(await parseError(response, fallbackMessage))
  }
}

/**
 * 認証状態を取得
 * @returns {Promise<object>}
 */
export async function getAuthStatus() {
  if (isWails) {
    return { authenticated: true }
  }

  const res = await apiFetch('/api/auth/status', {}, { skipAuthRedirect: true })
  const data = await res.json()
  rememberAuthToken(data)
  return data
}

/**
 * ログイン
 * @param {string} password
 * @param {string} totpCode
 * @returns {Promise<object>}
 */
export async function login(password, totpCode = '') {
  if (isWails) {
    return { authenticated: true, message: 'デスクトップモードでは認証不要です' }
  }

  const res = await apiFetch('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password, totp_code: totpCode })
  }, { skipAuthRedirect: true })

  const data = await res.json()
  if (!res.ok) {
    throw new Error(data?.error || 'ログインに失敗しました')
  }
  rememberAuthToken(data)
  return data
}

/**
 * 最近の再認証を行い、サーバーが発行したCSRFトークンを更新する。
 * @param {string} password
 * @returns {Promise<object>}
 */
export async function reauthenticate(password) {
  if (isWails) return { authenticated: true }

  const res = await apiFetch('/api/auth/reauth', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password })
  }, { skipAuthRedirect: true, skipReauth: true })
  const data = await res.json()
  // A wrong password should keep the confirmation dialog open, but the
  // session can expire while that dialog is displayed. In that case the
  // server marks the 401 explicitly and there is no session left to confirm;
  // clear the in-memory CSRF token and return to the full login flow (which
  // will request TOTP again when it is configured). App.vue handles the event
  // synchronously to purge sensitive state before navigation.
  if (res.status === 401 && data?.login_required) {
    csrfToken = null
    const expiryEvent = new CustomEvent('omni-money:session-expired', {
      cancelable: true,
      detail: { reason: 'session-expired' }
    })
    window.dispatchEvent(expiryEvent)
    // App.vue normally handles this synchronously so it can purge in-memory
    // financial state before navigating. Keep a fallback for a stale page or
    // a non-App caller that has no listener.
    if (!expiryEvent.defaultPrevented && window.location.pathname !== '/login') {
      window.location.replace('/login?reason=session-expired')
    }
    throw new Error('セッションの有効期限が切れました')
  }
  if (!res.ok) {
    throw new Error(data?.error || '再認証に失敗しました')
  }
  rememberAuthToken(data)
  return data
}

/**
 * Record visible, user-driven activity without returning session metadata.
 * Heartbeats are best-effort: a concurrent re-authentication may rotate the
 * session while one is in flight, so callers must not redirect on failure.
 * @returns {Promise<number>} HTTP status (204 means success)
 */
export async function keepAlive() {
  if (isWails) return 204

  const res = await apiFetch('/api/auth/keepalive', {
    method: 'POST'
  }, { skipAuthRedirect: true, skipReauth: true })
  return res.status
}

/**
 * ログアウト
 * @returns {Promise<void>}
 */
export async function logout() {
  if (isWails) return

  const res = await apiFetch('/api/auth/logout', {
    method: 'POST'
  }, { skipAuthRedirect: true })
  if (!res.ok) {
    throw new Error(await parseError(res, 'ログアウトに失敗しました'))
  }
  csrfToken = null
}

/**
 * 現在を含む全セッションを無効化する。
 * @returns {Promise<void>}
 */
export async function logoutAll() {
  if (isWails) return

  const res = await apiFetch('/api/auth/logout-all', {
    method: 'POST'
  }, { skipAuthRedirect: true })
  if (!res.ok) {
    throw new Error(await parseError(res, '全セッションのログアウトに失敗しました'))
  }
  csrfToken = null
}

/**
 * 口座リストを取得
 * @returns {Promise<string[]>}
 */
export async function getAccounts() {
  if (isWails) {
    return await window.go.main.App.GetAccounts()
  }
  const res = await apiFetch('/api/accounts')
  return await res.json()
}

/**
 * 項目リストを取得
 * @param {string} account
 * @returns {Promise<string[]>}
 */
export async function getItems(account = '') {
  if (isWails) {
    return await window.go.main.App.GetItems(account)
  }
  const params = account ? `?account=${encodeURIComponent(account)}` : ''
  const res = await apiFetch(`/api/items${params}`)
  return await res.json()
}

/**
 * 取引履歴を取得
 * @param {string} account
 * @param {string} search
 * @returns {Promise<object[]>}
 */
export async function getTransactions(account = '', search = '') {
  if (isWails) {
    return await window.go.main.App.GetTransactions(account, search)
  }
  const params = new URLSearchParams()
  if (account) params.set('account', account)
  if (search) params.set('search', search)
  const query = params.toString() ? `?${params.toString()}` : ''
  const res = await apiFetch(`/api/transactions${query}`)
  return await res.json()
}

/**
 * 取引を追加
 * @param {object} data
 * @returns {Promise<object>}
 */
export async function addTransaction(data) {
  if (isWails) {
    return await window.go.main.App.AddTransaction(data)
  }
  const res = await apiFetch('/api/transactions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  await throwIfNotOk(res, '取引の追加に失敗しました')
  return await res.json()
}

/**
 * 取引を更新
 * @param {number} id
 * @param {object} data
 * @returns {Promise<object>}
 */
export async function updateTransaction(id, data) {
  if (isWails) {
    return await window.go.main.App.UpdateTransaction(id, data)
  }
  const res = await apiFetch(`/api/transactions/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
  await throwIfNotOk(res, '取引の更新に失敗しました')
  return await res.json()
}

/**
 * 取引を削除
 * @param {number} id
 * @returns {Promise<void>}
 */
export async function deleteTransaction(id) {
  if (isWails) {
    return await window.go.main.App.DeleteTransaction(id)
  }
  await apiFetch(`/api/transactions/${id}`, { method: 'DELETE' })
}

/**
 * 残高推移を取得
 * @returns {Promise<object>}
 */
export async function getBalanceHistory() {
  if (isWails) {
    return await window.go.main.App.GetBalanceHistory()
  }
  const res = await apiFetch('/api/balance_history')
  return await res.json()
}

/**
 * フィルタリング済み残高推移を取得
 * @param {string[]} fundItems
 * @returns {Promise<object>}
 */
export async function getBalanceHistoryFiltered(fundItems) {
  if (isWails) {
    return await window.go.main.App.GetBalanceHistoryFiltered(fundItems)
  }
  const params = fundItems.map(i => `fund_items=${encodeURIComponent(i)}`).join('&')
  const res = await apiFetch(`/api/balance_history_filtered?${params}`)
  return await res.json()
}

/**
 * クレジットカード設定を取得
 * @returns {Promise<string[]>}
 */
export async function getCreditCardSettings() {
  if (isWails) {
    return await window.go.main.App.GetCreditCardSettings()
  }
  const res = await apiFetch('/api/credit_card_settings')
  return await res.json()
}

/**
 * クレジットカード設定を保存
 * @param {string[]} items
 * @returns {Promise<void>}
 */
export async function saveCreditCardSettings(items) {
  if (isWails) {
    return await window.go.main.App.SaveCreditCardSettings(items)
  }
  await apiFetch('/api/credit_card_settings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ credit_card_items: items })
  })
}

/**
 * 銀行口座設定を取得
 * @returns {Promise<string[]>}
 */
export async function getBankAccountSettings() {
  if (isWails) {
    return await window.go.main.App.GetBankAccountSettings()
  }
  const res = await apiFetch('/api/bank_account_settings')
  return await res.json()
}

/**
 * 銀行口座設定を保存
 * @param {string[]} items
 * @returns {Promise<void>}
 */
export async function saveBankAccountSettings(items) {
  if (isWails) {
    return await window.go.main.App.SaveBankAccountSettings(items)
  }
  await apiFetch('/api/bank_account_settings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ bank_account_items: items })
  })
}

/**
 * CSVバックアップを取得
 * @returns {Promise<string>}
 */
export async function backupToCSV() {
  if (isWails) {
    return await window.go.main.App.BackupToCSV()
  }
  const res = await apiFetch('/api/backup_csv')
  return await res.text()
}

/**
 * CSVバックアップファイルをダウンロードフォルダに保存
 * @returns {Promise<string>} - 保存先ファイルパス
 */
export async function backupToCSVFile() {
  if (isWails) {
    return await window.go.main.App.BackupToCSVFile()
  }
  // サーバーモード時はブラウザダウンロードにフォールバック
  const res = await apiFetch('/api/backup_csv')
  const csvContent = await res.text()
  const bom = '\uFEFF'
  const blob = new Blob([bom + csvContent], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `transactions_backup_${new Date().toISOString().slice(0, 10)}.csv`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return a.download
}

/**
 * CSVインポート
 * @param {string} content
 * @param {string} mode
 * @returns {Promise<number>}
 */
export async function importCSV(content, mode = 'append') {
  if (isWails) {
    return await window.go.main.App.ImportCSV(content, mode)
  }
  const res = await apiFetch('/api/import_csv', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, mode })
  })
  const data = await res.json()
  return data.imported_count
}

/**
 * スナップショットを作成
 * @returns {Promise<string>} - 作成されたスナップショットのパス
 */
export async function createSnapshot() {
  if (isWails) {
    return await window.go.main.App.CreateSnapshot()
  }
  const res = await apiFetch('/api/snapshots', { method: 'POST' })
  const data = await res.json()
  if (data.error) throw new Error(data.error)
  return data.path
}

/**
 * スナップショット一覧を取得
 * @returns {Promise<string[]>}
 */
export async function listSnapshots() {
  if (isWails) {
    return await window.go.main.App.ListSnapshots()
  }
  const res = await apiFetch('/api/snapshots')
  return await res.json()
}

/**
 * スナップショットから復元
 * @param {string} name - スナップショットファイル名
 * @returns {Promise<void>}
 */
export async function restoreSnapshot(name) {
  if (isWails) {
    return await window.go.main.App.RestoreSnapshot(name)
  }
  const res = await apiFetch(`/api/snapshots/restore`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })
  const data = await res.json()
  if (data.error) throw new Error(data.error)
}

// --- 画像関連 (Agent.md §6.5) ---

/**
 * 取引に画像を追加
 * @param {number} transactionId
 * @param {object} imageData - { filename, data (base64), mime_type }
 * @returns {Promise<object>}
 */
export async function addTransactionImage(transactionId, imageData) {
  if (isWails) {
    return await window.go.main.App.AddTransactionImage(transactionId, imageData)
  }
  const res = await apiFetch(`/api/transaction_images/${transactionId}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(imageData)
  })
  await throwIfNotOk(res, '画像の追加に失敗しました')
  return await res.json()
}

/**
 * 取引の画像一覧を取得
 * @param {number} transactionId
 * @returns {Promise<object[]>}
 */
export async function getTransactionImages(transactionId) {
  if (isWails) {
    return await window.go.main.App.GetTransactionImages(transactionId)
  }
  const res = await apiFetch(`/api/transaction_images/${transactionId}`)
  return await res.json()
}

/**
 * 取引から画像を削除
 * @param {number} transactionId
 * @param {number} imageId
 * @returns {Promise<void>}
 */
export async function deleteTransactionImage(transactionId, imageId) {
  if (isWails) {
    return await window.go.main.App.DeleteTransactionImage(imageId)
  }
  await apiFetch(`/api/transaction_images/${transactionId}/${imageId}`, { method: 'DELETE' })
}

// --- タグ関連 (Agent.md §6.6) ---

/**
 * タグ一覧を取得（ツリー構造）
 * @returns {Promise<object[]>}
 */
export async function getTags() {
  if (isWails) {
    return await window.go.main.App.GetTags()
  }
  const res = await apiFetch('/api/tags')
  return await res.json()
}

/**
 * タグを作成
 * @param {string} name
 * @param {number|null} parentId
 * @returns {Promise<object>}
 */
export async function createTag(name, parentId = null) {
  if (isWails) {
    return await window.go.main.App.CreateTag(name, parentId)
  }
  const res = await apiFetch('/api/tags', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, parent_id: parentId })
  })
  return await res.json()
}

/**
 * 「/」区切りのパスからタグを階層的に作成
 * @param {string} path - 例: "推し活/超かぐや姫！"
 * @returns {Promise<object>} 末端のタグ
 */
export async function createTagByPath(path) {
  if (isWails) {
    return await window.go.main.App.CreateTagByPath(path)
  }
  const res = await apiFetch('/api/tags/path', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path })
  })
  return await res.json()
}

/**
 * タグを更新
 * @param {number} id
 * @param {string} name
 * @returns {Promise<void>}
 */
export async function updateTag(id, name) {
  if (isWails) {
    return await window.go.main.App.UpdateTag(id, name)
  }
  await apiFetch(`/api/tags/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })
}

/**
 * タグを削除
 * @param {number} id
 * @returns {Promise<void>}
 */
export async function deleteTag(id) {
  if (isWails) {
    return await window.go.main.App.DeleteTag(id)
  }
  await apiFetch(`/api/tags/${id}`, { method: 'DELETE' })
}

/**
 * 取引に紐付いたタグを取得
 * @param {number} transactionId
 * @returns {Promise<object[]>}
 */
export async function getTransactionTags(transactionId) {
  if (isWails) {
    return await window.go.main.App.GetTransactionTags(transactionId)
  }
  const res = await apiFetch(`/api/transaction_tags/${transactionId}`)
  return await res.json()
}

/**
 * 取引にタグを追加
 * @param {number} transactionId
 * @param {number[]} tagIds
 * @returns {Promise<void>}
 */
export async function addTransactionTags(transactionId, tagIds) {
  if (isWails) {
    return await window.go.main.App.AddTransactionTags(transactionId, tagIds)
  }
  await apiFetch(`/api/transaction_tags/${transactionId}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tag_ids: tagIds })
  })
}

/**
 * 取引からタグを削除
 * @param {number} transactionId
 * @param {number} tagId
 * @returns {Promise<void>}
 */
export async function removeTransactionTag(transactionId, tagId) {
  if (isWails) {
    return await window.go.main.App.RemoveTransactionTag(transactionId, tagId)
  }
  await apiFetch(`/api/transaction_tags/${transactionId}/${tagId}`, { method: 'DELETE' })
}

/**
 * タグ別集計データを取得（円グラフ用）
 * @param {string} type - 'income' | 'expense' | ''
 * @param {string} startDate - YYYY-MM-DD
 * @param {string} endDate - YYYY-MM-DD
 * @returns {Promise<object[]>}
 */
export async function getTagSummary(type = '', startDate = '', endDate = '') {
  if (isWails) {
    return await window.go.main.App.GetTagSummary(type, startDate, endDate)
  }
  const params = new URLSearchParams()
  if (type) params.set('type', type)
  if (startDate) params.set('start_date', startDate)
  if (endDate) params.set('end_date', endDate)
  const query = params.toString() ? `?${params.toString()}` : ''
  const res = await apiFetch(`/api/tags/summary${query}`)
  return await res.json()
}

// --- 取引紐付け (Agent.md §6.2) ---

/**
 * 取引に紐付いた取引の一覧を取得
 * @param {number} transactionId
 * @returns {Promise<object[]>}
 */
export async function getTransactionLinks(transactionId) {
  if (isWails) {
    return await window.go.main.App.GetTransactionLinks(transactionId)
  }
  const res = await apiFetch(`/api/transaction_links/${transactionId}`)
  return await res.json()
}

/**
 * 取引同士を紐付ける
 * @param {number} transactionId
 * @param {number} linkedId
 * @returns {Promise<void>}
 */
export async function addTransactionLink(transactionId, linkedId) {
  if (isWails) {
    return await window.go.main.App.AddTransactionLink(transactionId, linkedId)
  }
  const res = await apiFetch(`/api/transaction_links/${transactionId}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ linked_id: linkedId })
  })
  await throwIfNotOk(res, '紐付けに失敗しました')
}

/**
 * 取引の紐付けを解除する
 * @param {number} transactionId
 * @param {number} linkedId
 * @returns {Promise<void>}
 */
export async function removeTransactionLink(transactionId, linkedId) {
  if (isWails) {
    return await window.go.main.App.RemoveTransactionLink(transactionId, linkedId)
  }
  const res = await apiFetch(`/api/transaction_links/${transactionId}/${linkedId}`, { method: 'DELETE' })
  await throwIfNotOk(res, '紐付け解除に失敗しました')
}
