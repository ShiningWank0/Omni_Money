// Wailsバインディングへのラッパー関数
// デスクトップモード時はWailsのGoバインディングを直接呼び出し、
// サーバーモード時はREST APIを呼び出すよう抽象化する

const isWails = typeof window.go !== 'undefined'

/**
 * 口座リストを取得
 * @returns {Promise<string[]>}
 */
export async function getAccounts() {
  if (isWails) {
    return await window.go.main.App.GetAccounts()
  }
  const res = await fetch('/api/accounts')
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
  const res = await fetch(`/api/items${params}`)
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
  const res = await fetch(`/api/transactions${query}`)
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
  const res = await fetch('/api/transactions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
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
  const res = await fetch(`/api/transactions/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  })
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
  await fetch(`/api/transactions/${id}`, { method: 'DELETE' })
}

/**
 * 残高推移を取得
 * @returns {Promise<object>}
 */
export async function getBalanceHistory() {
  if (isWails) {
    return await window.go.main.App.GetBalanceHistory()
  }
  const res = await fetch('/api/balance_history')
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
  const res = await fetch(`/api/balance_history_filtered?${params}`)
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
  const res = await fetch('/api/credit_card_settings')
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
  await fetch('/api/credit_card_settings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ credit_card_items: items })
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
  const res = await fetch('/api/backup_csv')
  return await res.text()
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
  const res = await fetch('/api/import_csv', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, mode })
  })
  const data = await res.json()
  return data.imported_count
}
