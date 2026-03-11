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
 * CSVバックアップファイルをダウンロードフォルダに保存
 * @returns {Promise<string>} - 保存先ファイルパス
 */
export async function backupToCSVFile() {
  if (isWails) {
    return await window.go.main.App.BackupToCSVFile()
  }
  // サーバーモード時はブラウザダウンロードにフォールバック
  const res = await fetch('/api/backup_csv')
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
  const res = await fetch('/api/import_csv', {
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
  const res = await fetch('/api/snapshots', { method: 'POST' })
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
  const res = await fetch('/api/snapshots')
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
  const res = await fetch(`/api/snapshots/restore`, {
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
  const res = await fetch(`/api/transaction_images/${transactionId}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(imageData)
  })
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
  const res = await fetch(`/api/transaction_images/${transactionId}`)
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
  await fetch(`/api/transaction_images/${transactionId}/${imageId}`, { method: 'DELETE' })
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
  const res = await fetch('/api/tags')
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
  const res = await fetch('/api/tags', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, parent_id: parentId })
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
  await fetch(`/api/tags/${id}`, {
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
  await fetch(`/api/tags/${id}`, { method: 'DELETE' })
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
  const res = await fetch(`/api/transaction_tags/${transactionId}`)
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
  await fetch(`/api/transaction_tags/${transactionId}`, {
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
  await fetch(`/api/transaction_tags/${transactionId}/${tagId}`, { method: 'DELETE' })
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
  const res = await fetch(`/api/tags/summary${query}`)
  return await res.json()
}

