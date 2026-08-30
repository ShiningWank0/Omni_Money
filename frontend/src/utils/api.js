import { authenticatePasskey, createPasskey } from './passkeys.js'

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

function bytesToBase64(bytes) {
  let binary = ''
  for (const value of bytes) binary += String.fromCharCode(value)
  return window.btoa(binary)
}

function textToBase64(value) {
  const bytes = new TextEncoder().encode(value)
  try {
    return bytesToBase64(bytes)
  } finally {
    bytes.fill(0)
  }
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
    const skipPaths = new Set([
      '/api/auth/login',
      '/api/auth/status',
      '/api/auth/setup',
      '/api/auth/invitations/accept',
      '/api/auth/password-reset/complete'
    ])
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
    const status = await getDesktopVaultStatus()
    const authenticated = status?.state ? status.state === 'unlocked' : status?.unlocked === true
    return { ...status, authenticated }
  }

  const res = await apiFetch('/api/auth/status', {}, { skipAuthRedirect: true })
  const data = await res.json()
  rememberAuthToken(data)
  return data
}

export async function getDesktopVaultStatus() {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.GetDesktopVaultStatus()
}

export async function setupDesktopVault(password) {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.SetupDesktopVault(password)
}

export async function migrateLegacyDesktopVault(password) {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.MigrateLegacyDesktopVault(password)
}

export async function acknowledgeDesktopVaultRecovery() {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.AcknowledgeDesktopVaultRecovery()
}

export async function unlockDesktopVault(password) {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.UnlockDesktopVault(password)
}

export async function recoverDesktopVault(recoveryCode, newPassword) {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.RecoverDesktopVault(recoveryCode, newPassword)
}

export async function lockDesktopVault() {
  if (!isWails) throw new Error('この操作はDesktopモード専用です')
  return await window.go.main.App.LockDesktopVault()
}

/**
 * ログイン
 * @param {string} email
 * @param {string} password
 * @returns {Promise<object>}
 */
export async function login(email, password) {
  if (isWails) {
    return { authenticated: true, message: 'デスクトップモードでは認証不要です' }
  }

  const res = await apiFetch('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password_b64: textToBase64(password) })
  }, { skipAuthRedirect: true })

  const data = await res.json()
  if (!res.ok) {
    throw new Error(data?.error || 'ログインに失敗しました')
  }
  rememberAuthToken(data)
  return data
}

export async function loginWithPasskey(email) {
  if (isWails) throw new Error('パスキー認証はサーバーモード専用です')
  const begin = await apiFetch('/api/auth/passkeys/login/begin', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email })
  }, { skipAuthRedirect: true, skipReauth: true })
  await throwIfNotOk(begin, 'パスキー認証を開始できませんでした')
  const ceremony = await begin.json()
  const assertion = await authenticatePasskey(ceremony.options)
  try {
    const finish = await apiFetch('/api/auth/passkeys/login/finish', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ceremony_id: ceremony.ceremony_id,
        credential: assertion.credential,
        prf_result_b64: bytesToBase64(assertion.prfResult)
      })
    }, { skipAuthRedirect: true, skipReauth: true })
    const data = await finish.json()
    if (!finish.ok) throw new Error(data?.error || 'パスキー認証に失敗しました')
    rememberAuthToken(data)
    return data
  } finally {
    assertion.prfResult.fill(0)
  }
}

export async function listPasskeys() {
  if (isWails) return []
  const response = await apiFetch('/api/auth/passkeys')
  await throwIfNotOk(response, 'パスキー一覧を取得できませんでした')
  const data = await response.json()
  return Array.isArray(data?.passkeys) ? data.passkeys : []
}

export async function registerPasskey({ name, password }) {
  if (isWails) throw new Error('パスキー登録はサーバーモード専用です')
  const begin = await apiFetch('/api/auth/passkeys/register/begin', { method: 'POST' })
  await throwIfNotOk(begin, 'パスキー登録を開始できませんでした')
  const ceremony = await begin.json()
  const created = await createPasskey(ceremony.options)
  try {
    const finish = await apiFetch('/api/auth/passkeys/register/finish', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ceremony_id: ceremony.ceremony_id,
        name,
        password_b64: textToBase64(password),
        credential: created.credential,
        prf_result_b64: bytesToBase64(created.prfResult)
      })
    }, { skipReauth: true })
    await throwIfNotOk(finish, 'パスキーを登録できませんでした')
    return await finish.json()
  } finally {
    created.prfResult.fill(0)
  }
}

export async function deletePasskey(id) {
  if (isWails) throw new Error('パスキー削除はサーバーモード専用です')
  const response = await apiFetch(`/api/auth/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!response.ok) throw new Error(await parseError(response, 'パスキーを削除できませんでした'))
}

/**
 * Create the one and only initial administrator. recoverySecret must be a
 * client-generated 32-byte value that the user saved before this call.
 */
export async function setupInitialAdmin({ setupToken, email, displayName, password, recoverySecret }) {
  if (isWails) return { user: null }

  const res = await apiFetch('/api/auth/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      setup_token_b64: textToBase64(setupToken),
      email,
      display_name: displayName,
      password_b64: textToBase64(password),
      recovery_secret_b64: bytesToBase64(recoverySecret)
    })
  }, { skipAuthRedirect: true, skipReauth: true })

  const data = await res.json()
  if (!res.ok) {
    throw new Error(data?.error || '初期管理者の作成に失敗しました')
  }
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
    body: JSON.stringify({ password_b64: textToBase64(password) })
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

export async function reauthenticateWithPasskey() {
  if (isWails) return { authenticated: true }
  const begin = await apiFetch('/api/auth/passkeys/reauth/begin', {
    method: 'POST'
  }, { skipAuthRedirect: true, skipReauth: true })
  await throwIfNotOk(begin, 'パスキー再認証を開始できませんでした')
  const ceremony = await begin.json()
  const assertion = await authenticatePasskey(ceremony.options)
  try {
    const finish = await apiFetch('/api/auth/passkeys/reauth/finish', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ceremony_id: ceremony.ceremony_id,
        credential: assertion.credential,
        prf_result_b64: bytesToBase64(assertion.prfResult)
      })
    }, { skipAuthRedirect: true, skipReauth: true })
    const data = await finish.json()
    if (finish.status === 401 && data?.login_required) {
      csrfToken = null
      window.dispatchEvent(new CustomEvent('omni-money:session-expired', {
        cancelable: true,
        detail: { reason: 'session-expired' }
      }))
      throw new Error('セッションの有効期限が切れました')
    }
    if (!finish.ok) throw new Error(data?.error || 'パスキー再認証に失敗しました')
    rememberAuthToken(data)
    return data
  } finally {
    assertion.prfResult.fill(0)
  }
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
 * 招待を受諾する。token はURLではなくユーザーが貼り付けた値を渡す。
 */
export async function acceptServerInvitation({ token, displayName, password, recoverySecret }) {
  if (isWails) throw new Error('この操作はサーバーモード専用です')

  const res = await apiFetch('/api/auth/invitations/accept', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      token_b64: textToBase64(token),
      display_name: displayName,
      password_b64: textToBase64(password),
      recovery_secret_b64: bytesToBase64(recoverySecret)
    })
  }, { skipAuthRedirect: true, skipReauth: true })
  await throwIfNotOk(res, '招待を受諾できませんでした')
  return await res.json()
}

/**
 * 管理者から受け取ったreset tokenと既存回復secretでcredentialを更新する。
 */
export async function completeServerPasswordReset({ token, recoverySecret, newPassword, newRecoverySecret }) {
  if (isWails) throw new Error('この操作はサーバーモード専用です')

  const res = await apiFetch('/api/auth/password-reset/complete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      token_b64: textToBase64(token),
      recovery_secret_b64: bytesToBase64(recoverySecret),
      new_password_b64: textToBase64(newPassword),
      new_recovery_secret_b64: bytesToBase64(newRecoverySecret)
    })
  }, { skipAuthRedirect: true, skipReauth: true })
  await throwIfNotOk(res, 'パスワードを再設定できませんでした')
  return await res.json()
}

export async function listServerUsers() {
  if (isWails) return []
  const res = await apiFetch('/api/admin/users')
  await throwIfNotOk(res, 'ユーザー一覧を取得できませんでした')
  const data = await res.json()
  return Array.isArray(data?.users) ? data.users : []
}

export async function createServerInvitation({ email, role = 'user', expiresInSeconds = 86400 }) {
  if (isWails) throw new Error('この操作はサーバーモード専用です')
  const res = await apiFetch('/api/admin/invitations', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, role, expires_in_seconds: expiresInSeconds })
  })
  await throwIfNotOk(res, '招待を作成できませんでした')
  return await res.json()
}

export async function disableServerUser(userID) {
  if (isWails) throw new Error('この操作はサーバーモード専用です')
  const res = await apiFetch(`/api/admin/users/${encodeURIComponent(userID)}/disable`, {
    method: 'POST'
  })
  await throwIfNotOk(res, 'ユーザーを無効化できませんでした')
}

export async function createServerPasswordReset(userID, expiresInSeconds = 900) {
  if (isWails) throw new Error('この操作はサーバーモード専用です')
  const res = await apiFetch('/api/admin/password-resets', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ target_user_id: userID, expires_in_seconds: expiresInSeconds })
  })
  await throwIfNotOk(res, 'パスワード再設定を開始できませんでした')
  return await res.json()
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
 * CSVバックアップを取得。拡張データが存在する場合は、画像・タグ・
 * タグ紐付け・取引リンク・ledger設定を含むCSV v3を返す。
 * @returns {Promise<string>}
 */
export async function backupToCSV() {
  if (isWails) {
    throw new Error('Desktop版のCSVは保存先を選ぶ安全なファイル出力のみ利用できます')
  }
  const res = await apiFetch('/api/backup_csv')
  await validateCSVResponse(res)
  return await readBoundedCSVText(res, 64 * 1024 * 1024)
}

async function validateCSVResponse(response) {
  await throwIfNotOk(response, 'CSVバックアップの取得に失敗しました')

  const contentType = response.headers.get('Content-Type') || ''
  if (!/^text\/csv(?:\s*;|$)/i.test(contentType)) {
    throw new Error('CSVではない応答を受信したため、バックアップを中止しました')
  }
}

async function readBoundedCSVText(response, maxBytes) {
  const contentLengthHeader = response.headers.get('Content-Length')
  const contentLength = contentLengthHeader == null ? NaN : Number(contentLengthHeader)
  if (Number.isFinite(contentLength) && (contentLength < 0 || contentLength > maxBytes)) {
    throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
  }
  if (response.body && typeof response.body.getReader === 'function') {
    const reader = response.body.getReader()
    const decoder = new TextDecoder('utf-8', { fatal: true })
    const chunks = []
    let total = 0
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        if (!value) continue
        total += value.byteLength
        if (total > maxBytes) {
          await reader.cancel()
          throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
        }
        chunks.push(decoder.decode(value, { stream: true }))
      }
      chunks.push(decoder.decode())
      return chunks.join('')
    } catch (error) {
      try { await reader.cancel() } catch (_) { /* best effort */ }
      throw error
    }
  }
  const bytes = new Uint8Array(await response.arrayBuffer())
  if (bytes.byteLength > maxBytes) {
    throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
  }
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch (_) {
    throw new Error('CSVバックアップは有効なUTF-8ではありません')
  }
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
  await validateCSVResponse(res)

  const downloadName = `transactions_backup_${new Date().toISOString().slice(0, 10)}.csv`
	if (typeof window.showSaveFilePicker === 'function' && res.body) {
		let writable = null
		try {
			const handle = await window.showSaveFilePicker({
				suggestedName: downloadName,
				types: [{ description: 'CSV', accept: { 'text/csv': ['.csv'] } }]
			})
			writable = await handle.createWritable()
			await res.body.pipeTo(writable)
			return downloadName
		} catch (error) {
			try { await writable?.abort() } catch (_) { /* best effort */ }
			// A canceled picker or createWritable failure can leave the response
			// body unread. Explicitly cancel it so the server can release its
			// private export spool and weighted admission slot.
			try { await res.body?.cancel?.() } catch (_) { /* best effort */ }
			throw error
		}
	}
	// Without a writable file stream, a Blob download necessarily keeps the
	// response chunks and the final Blob live together. Keep this compatibility
	// path deliberately small and reject a large response before any body
	// allocation when the server provided Content-Length. Full-size exports
	// require showSaveFilePicker or the Desktop file API.
	const browserBlobCap = 64 * 1024 * 1024
	const contentLengthHeader = res.headers.get('Content-Length')
	const contentLength = contentLengthHeader == null ? NaN : Number(contentLengthHeader)
	if (Number.isFinite(contentLength) && (contentLength < 0 || contentLength + 3 > browserBlobCap)) {
		try { await res.body?.cancel?.() } catch (_) { /* best effort */ }
		throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
	}
  let objectURL = null
  let anchor = null
  try {
    const chunks = []
    let total = 3
    if (res.body && typeof res.body.getReader === 'function') {
      const reader = res.body.getReader()
      try {
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          if (value) {
            total += value.byteLength
            if (total > browserBlobCap) {
              await reader.cancel()
              throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
            }
            chunks.push(value)
          }
        }
      } catch (error) {
        try { await reader.cancel() } catch (_) { /* best effort */ }
        throw error
      }
    } else {
      const bytes = new Uint8Array(await res.arrayBuffer())
      total += bytes.byteLength
      if (total > browserBlobCap) {
        throw new Error('CSVバックアップが大きすぎます。ファイル保存を使用してください')
      }
      chunks.push(bytes)
    }
    const blob = new Blob([new Uint8Array([0xEF, 0xBB, 0xBF]), ...chunks], { type: 'text/csv;charset=utf-8;' })
    objectURL = URL.createObjectURL(blob)
    anchor = document.createElement('a')
    anchor.href = objectURL
    anchor.download = downloadName
    document.body.appendChild(anchor)
    anchor.click()
    return downloadName
  } finally {
    anchor?.remove()
    if (objectURL) URL.revokeObjectURL(objectURL)
  }
}

/**
 * CSVインポート（旧v1/v2 transactions-only形式と、完全なv3形式に対応）
 * @param {string|Blob} content - string is the bounded Wails/JSON compatibility path; Blob streams as raw CSV.
 * @param {string} mode
 * @returns {Promise<number>}
 */
export async function importCSV(content, mode = 'append') {
  if (isWails) {
    // The native binding owns the file descriptor and OS picker.  Keep the
    // string call below solely for older Wails clients that have no file API.
    if (content == null && typeof window.go.main.App.ImportCSVFile === 'function') {
      return await window.go.main.App.ImportCSVFile(mode)
    }
    let text
    if (typeof content === 'string') {
      text = content
    } else {
      if (content.size > 64 * 1024 * 1024) throw new Error('CSVファイルが大きすぎます')
      try {
        text = new TextDecoder('utf-8', { fatal: true }).decode(await content.arrayBuffer())
      } catch (_) {
        throw new Error('CSVファイルは有効なUTF-8で指定してください')
      }
    }
    return await window.go.main.App.ImportCSV(text, mode)
  }
  if (content instanceof Blob) {
    const maxCSVBytes = 512 * 1024 * 1024
    if (content.size > maxCSVBytes) {
      throw new Error('CSVファイルが大きすぎます')
    }
    const res = await apiFetch(`/api/import_csv?mode=${encodeURIComponent(mode)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'text/csv' },
      body: content
    })
    await throwIfNotOk(res, 'CSVインポートに失敗しました')
    const data = await res.json()
    if (!Number.isInteger(data?.imported_count) || data.imported_count < 0) {
      throw new Error('CSVインポートの応答が不正です')
    }
    return data.imported_count
  }
  const res = await apiFetch('/api/import_csv', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, mode })
  })
  await throwIfNotOk(res, 'CSVインポートに失敗しました')
  const data = await res.json()
  if (!Number.isInteger(data?.imported_count) || data.imported_count < 0) {
    throw new Error('CSVインポートの応答が不正です')
  }
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
  await throwIfNotOk(res, 'タグ一覧の取得に失敗しました')
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
  await throwIfNotOk(res, 'タグの作成に失敗しました')
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
  await throwIfNotOk(res, 'タグ階層の作成に失敗しました')
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
  const res = await apiFetch(`/api/tags/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })
  await throwIfNotOk(res, 'タグ名の変更に失敗しました')
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
  const res = await apiFetch(`/api/tags/${id}`, { method: 'DELETE' })
  await throwIfNotOk(res, 'タグの削除に失敗しました')
}

/**
 * タグ削除で影響する子タグ・取引件数を確認
 * @param {number} id
 * @returns {Promise<object>}
 */
export async function getTagDeleteImpact(id) {
  if (isWails) {
    return await window.go.main.App.GetTagDeleteImpact(id)
  }
  const res = await apiFetch(`/api/tags/${id}/impact`)
  await throwIfNotOk(res, 'タグ削除の影響確認に失敗しました')
  return await res.json()
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
