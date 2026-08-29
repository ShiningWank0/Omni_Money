<template>
  <div class="login-root">
    <div class="login-container">
      <div class="login-card">
        <div class="login-header">
          <h1 class="app-title">Omni Money</h1>
          <p class="app-subtitle">{{ pageTitle }}</p>
        </div>

        <div v-if="!setupRequired" class="auth-mode-tabs" aria-label="アカウント操作">
          <button type="button" :class="{ active: mode === 'login' }" @click="setMode('login')">ログイン</button>
          <button type="button" :class="{ active: mode === 'invite' }" @click="setMode('invite')">招待を受諾</button>
          <button type="button" :class="{ active: mode === 'reset' }" @click="setMode('reset')">再設定を完了</button>
        </div>

        <form @submit.prevent="handleSubmit">
          <div v-if="errorMessage" class="error-message" role="alert">{{ errorMessage }}</div>
          <div v-if="infoMessage" class="info-message" role="status">{{ infoMessage }}</div>

          <div v-if="setupRequired" class="form-group">
            <label for="setup-token" class="form-label">初期設定トークン</label>
            <input id="setup-token" v-model="setupToken" type="password" class="form-input" autocomplete="off" maxlength="512" required>
          </div>

          <div v-if="mode === 'invite' || mode === 'reset'" class="token-notice">
            管理者から安全な経路で受け取ったtokenを貼り付けてください。tokenをURLへ入れたり、この端末へ保存したりしないでください。
          </div>
          <div v-if="mode === 'invite' || mode === 'reset'" class="form-group">
            <label for="operation-token" class="form-label">{{ mode === 'invite' ? '招待token' : '再設定token' }}</label>
            <input id="operation-token" v-model.trim="operationToken" type="password" class="form-input" autocomplete="off" maxlength="4096" required>
          </div>

          <div v-if="mode === 'login'" class="form-group">
            <label for="email" class="form-label">メールアドレス</label>
            <input id="email" v-model.trim="email" type="email" class="form-input" autocomplete="username" maxlength="254" required>
          </div>

          <div v-if="setupRequired || mode === 'invite'" class="form-group">
            <label for="display-name" class="form-label">表示名</label>
            <input id="display-name" v-model.trim="displayName" type="text" class="form-input" autocomplete="name" maxlength="120" required>
          </div>

          <div v-if="mode === 'reset'" class="form-group">
            <label for="current-recovery" class="form-label">現在の回復コード</label>
            <input id="current-recovery" v-model.trim="currentRecoveryCode" type="password" class="form-input" autocomplete="off" maxlength="128" required>
            <p class="field-hint">管理者はこの値を知ることも、代わりに復元することもできません。</p>
          </div>

          <div class="form-group">
            <label for="password" class="form-label">{{ mode === 'reset' ? '新しいパスワード' : 'パスワード' }}</label>
            <input
              id="password"
              v-model="password"
              type="password"
              class="form-input"
              :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
              minlength="12"
              maxlength="256"
              required
            >
          </div>

          <div v-if="setupRequired || mode !== 'login'" class="form-group">
            <label for="password-confirmation" class="form-label">パスワード（確認）</label>
            <input id="password-confirmation" v-model="passwordConfirmation" type="password" class="form-input" autocomplete="new-password" minlength="12" maxlength="256" required>
          </div>

          <div v-if="needsNewRecovery" class="recovery-panel">
            <div class="form-label">{{ mode === 'reset' ? '新しい回復コード' : '回復コード' }}</div>
            <p class="recovery-warning">
              パスワードを忘れた場合に既存データを開く唯一のコードです。管理者でも復元できません。
              パスワードマネージャー等の安全な場所へ保存してください。
            </p>
            <textarea class="recovery-code" :value="newRecoveryCode" readonly rows="3" aria-label="新しい回復コード" @focus="$event.target.select()"></textarea>
            <button type="button" class="secondary-button" @click="copyRecoveryCode">回復コードをコピー</button>
            <label class="confirmation-label">
              <input v-model="recoverySaved" type="checkbox" required>
              安全な場所へ保存しました
            </label>
          </div>

          <button type="submit" class="login-button" :disabled="loading">
            <span v-if="loading" class="loading-spinner"></span>
            <span>{{ submitLabel }}</span>
          </button>

          <template v-if="mode === 'login' && !setupRequired">
            <div class="auth-divider"><span>または</span></div>
            <button type="button" class="passkey-button" :disabled="loading || !canUsePasskeys" @click="handlePasskeyLogin">
              パスキーでログイン
            </button>
            <p v-if="!canUsePasskeys" class="field-hint passkey-hint">
              パスキーはHTTPS接続の対応ブラウザで利用できます。
            </p>
          </template>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  acceptServerInvitation,
  completeServerPasswordReset,
  getAuthStatus,
  isWailsMode,
  login,
  loginWithPasskey,
  setupInitialAdmin
} from '../utils/api'
import { passkeysSupported } from '../utils/passkeys'
import {
  destroySecretBytes,
  generateRecoverySecret,
  recoveryCodeToSecret,
  recoverySecretToCode
} from '../utils/recovery'

const requestedMode = new URLSearchParams(window.location.search).get('mode')
const mode = ref(['invite', 'reset'].includes(requestedMode) ? requestedMode : 'login')
const email = ref('')
const password = ref('')
const passwordConfirmation = ref('')
const displayName = ref('')
const setupToken = ref('')
const operationToken = ref('')
const currentRecoveryCode = ref('')
const setupRequired = ref(false)
const newRecoveryBytes = ref(null)
const newRecoveryCode = ref('')
const recoverySaved = ref(false)
const loading = ref(false)
const errorMessage = ref('')
const infoMessage = ref('')
const canUsePasskeys = passkeysSupported()

const needsNewRecovery = computed(() => setupRequired.value || mode.value === 'invite' || mode.value === 'reset')
const pageTitle = computed(() => {
  if (setupRequired.value) return '最初の管理者を作成'
  if (mode.value === 'invite') return '招待されたアカウントを作成'
  if (mode.value === 'reset') return 'パスワードと回復コードを再設定'
  return 'サーバーモードにログイン'
})
const submitLabel = computed(() => {
  if (loading.value) return '処理中...'
  if (setupRequired.value) return '管理者を作成してログイン'
  if (mode.value === 'invite') return '招待を受諾'
  if (mode.value === 'reset') return '再設定を完了'
  return 'ログイン'
})

function prepareRecoveryCode() {
  destroySecretBytes(newRecoveryBytes.value)
  const bytes = generateRecoverySecret()
  newRecoveryBytes.value = bytes
  newRecoveryCode.value = recoverySecretToCode(bytes)
  recoverySaved.value = false
}

function clearFormSecrets({ clearGenerated = true } = {}) {
  password.value = ''
  passwordConfirmation.value = ''
  setupToken.value = ''
  operationToken.value = ''
  currentRecoveryCode.value = ''
  recoverySaved.value = false
  if (clearGenerated) {
    destroySecretBytes(newRecoveryBytes.value)
    newRecoveryBytes.value = null
    newRecoveryCode.value = ''
  }
}

function setMode(nextMode, message = '') {
  clearFormSecrets()
  mode.value = nextMode
  errorMessage.value = ''
  infoMessage.value = message
  const suffix = nextMode === 'login' ? '' : `?mode=${nextMode}`
  window.history.replaceState({}, '', `/login${suffix}`)
  if (nextMode !== 'login') prepareRecoveryCode()
}

onMounted(async () => {
  if (isWailsMode) {
    window.location.href = '/'
    return
  }
  try {
    const status = await getAuthStatus()
    setupRequired.value = Boolean(status?.setup_required)
    if (setupRequired.value) {
      mode.value = 'login'
      window.history.replaceState({}, '', '/login')
      prepareRecoveryCode()
    } else if (status?.authenticated && mode.value === 'login') {
      window.location.href = '/'
      return
    } else if (mode.value !== 'login') {
      prepareRecoveryCode()
    }
  } catch {
    if (mode.value !== 'login') prepareRecoveryCode()
  }
})

onBeforeUnmount(() => clearFormSecrets())

async function copyRecoveryCode() {
  try {
    await navigator.clipboard.writeText(newRecoveryCode.value)
    infoMessage.value = '回復コードをクリップボードへコピーしました。保存後はクリップボードも消去してください'
  } catch {
    errorMessage.value = 'コピーできませんでした。回復コードを選択して保存してください'
  }
}

function requireMatchingNewCredentials() {
  if (password.value !== passwordConfirmation.value) throw new Error('確認用パスワードが一致しません')
  if (!recoverySaved.value || !newRecoveryBytes.value) throw new Error('回復コードを安全な場所へ保存してください')
}

async function handleSubmit() {
  loading.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  let currentRecoveryBytes = null
  let completed = false
  try {
    if (setupRequired.value) {
      requireMatchingNewCredentials()
      await setupInitialAdmin({
        setupToken: setupToken.value,
        email: email.value,
        displayName: displayName.value,
        password: password.value,
        recoverySecret: newRecoveryBytes.value
      })
      // Bootstrap is committed independently from login. Never leave the UI
      // offering bootstrap again if only session creation/login fails.
      setupRequired.value = false
      mode.value = 'login'
      window.history.replaceState({}, '', '/login')
      destroySecretBytes(newRecoveryBytes.value)
      newRecoveryBytes.value = null
      newRecoveryCode.value = ''
      recoverySaved.value = false
      try {
        await login(email.value, password.value)
      } catch {
        infoMessage.value = '管理者アカウントは作成済みです。設定したパスワードでログインしてください'
        throw new Error('管理者の作成後に自動ログインできませんでした')
      }
      completed = true
      window.location.href = '/'
      return
    }
    if (mode.value === 'invite') {
      requireMatchingNewCredentials()
      const result = await acceptServerInvitation({
        token: operationToken.value,
        displayName: displayName.value,
        password: password.value,
        recoverySecret: newRecoveryBytes.value
      })
      email.value = result?.user?.email || ''
      completed = true
      setMode('login', 'アカウントを作成しました。設定したパスワードでログインしてください')
      return
    }
    if (mode.value === 'reset') {
      requireMatchingNewCredentials()
      currentRecoveryBytes = recoveryCodeToSecret(currentRecoveryCode.value)
      await completeServerPasswordReset({
        token: operationToken.value,
        recoverySecret: currentRecoveryBytes,
        newPassword: password.value,
        newRecoverySecret: newRecoveryBytes.value
      })
      completed = true
      setMode('login', '再設定が完了しました。新しいパスワードでログインしてください')
      return
    }
    await login(email.value, password.value)
    completed = true
    window.location.href = '/'
  } catch (error) {
    errorMessage.value = error?.message || '操作に失敗しました'
  } finally {
    destroySecretBytes(currentRecoveryBytes)
    password.value = ''
    passwordConfirmation.value = ''
    setupToken.value = ''
    currentRecoveryCode.value = ''
    if (completed) {
      operationToken.value = ''
      destroySecretBytes(newRecoveryBytes.value)
      newRecoveryBytes.value = null
      newRecoveryCode.value = ''
    }
    loading.value = false
  }
}

async function handlePasskeyLogin() {
  if (!email.value.trim()) {
    errorMessage.value = 'メールアドレスを入力してください'
    return
  }
  loading.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    await loginWithPasskey(email.value.trim())
    window.location.href = '/'
  } catch (error) {
    errorMessage.value = error?.message || 'パスキー認証に失敗しました'
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-root { width: 100%; min-height: calc(100vh - 2rem); display: flex; align-items: center; justify-content: center; overflow: auto; }
.login-container { width: 100%; max-width: 440px; padding: 1rem; }
.login-card { background: rgba(255, 255, 255, 0.94); border-radius: 20px; box-shadow: 0 10px 30px rgba(0, 0, 0, 0.2); backdrop-filter: blur(10px); padding: 2rem; box-sizing: border-box; }
.login-header { text-align: center; margin-bottom: 1.25rem; }
.app-title { font-size: 2rem; font-weight: 700; margin: 0 0 0.4rem; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); -webkit-background-clip: text; -webkit-text-fill-color: transparent; background-clip: text; }
.app-subtitle { margin: 0; color: #666; }
.auth-mode-tabs { display: grid; grid-template-columns: repeat(3, 1fr); gap: 0.35rem; margin-bottom: 1rem; }
.auth-mode-tabs button { border: 1px solid rgba(102, 126, 234, 0.3); border-radius: 8px; padding: 0.55rem 0.25rem; background: #fff; color: #4d5fc7; cursor: pointer; }
.auth-mode-tabs button.active { color: #fff; background: #667eea; }
.form-group { margin-bottom: 1rem; }
.form-label { display: block; margin-bottom: 0.4rem; color: #333; font-size: 0.9rem; font-weight: 600; }
.form-input { width: 100%; padding: 0.75rem 1rem; border: 2px solid rgba(102, 126, 234, 0.2); border-radius: 10px; box-sizing: border-box; }
.form-input:focus { outline: none; border-color: #667eea; box-shadow: 0 0 20px rgba(102, 126, 234, 0.2); }
.field-hint { margin: 0.35rem 0 0; color: #666; font-size: 0.8rem; line-height: 1.4; }
.token-notice { margin-bottom: 1rem; padding: 0.75rem; border-radius: 8px; background: #fff8e8; color: #694d00; font-size: 0.84rem; line-height: 1.45; }
.login-button { width: 100%; padding: 0.75rem; border: none; border-radius: 10px; color: #fff; font-size: 1rem; font-weight: 600; cursor: pointer; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }
.login-button:disabled { opacity: 0.65; cursor: not-allowed; }
.auth-divider { display: flex; align-items: center; gap: 0.75rem; margin: 1rem 0; color: #777; font-size: 0.8rem; }
.auth-divider::before, .auth-divider::after { content: ''; flex: 1; height: 1px; background: rgba(102, 126, 234, 0.24); }
.passkey-button { width: 100%; padding: 0.75rem; border: 1px solid #667eea; border-radius: 10px; color: #4d5fc7; font-size: 1rem; font-weight: 600; cursor: pointer; background: #fff; }
.passkey-button:disabled { opacity: 0.55; cursor: not-allowed; }
.passkey-hint { text-align: center; }
.error-message, .info-message { padding: 0.75rem 1rem; border-radius: 10px; margin-bottom: 1rem; font-size: 0.9rem; }
.error-message { background: rgba(255, 69, 58, 0.1); border: 1px solid rgba(255, 69, 58, 0.3); color: #d70015; }
.info-message { background: rgba(52, 199, 89, 0.1); border: 1px solid rgba(52, 199, 89, 0.3); color: #176b2c; }
.recovery-panel { margin: 1rem 0; padding: 1rem; border: 1px solid rgba(102, 126, 234, 0.25); border-radius: 10px; background: rgba(102, 126, 234, 0.06); }
.recovery-warning { margin: 0 0 0.75rem; color: #4a4a4a; font-size: 0.86rem; line-height: 1.45; }
.recovery-code { width: 100%; box-sizing: border-box; resize: none; overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.secondary-button { margin-top: 0.5rem; padding: 0.5rem 0.75rem; border: 1px solid #667eea; border-radius: 8px; color: #4d5fc7; background: #fff; cursor: pointer; }
.confirmation-label { display: flex; gap: 0.5rem; align-items: center; margin-top: 0.85rem; font-size: 0.9rem; color: #333; }
.loading-spinner { display: inline-block; margin-right: 0.5rem; width: 14px; height: 14px; border: 2px solid rgba(255, 255, 255, 0.3); border-top: 2px solid #fff; border-radius: 50%; animation: spin 1s linear infinite; vertical-align: text-bottom; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
@media (max-width: 480px) { .login-card { padding: 1.25rem; } .auth-mode-tabs { grid-template-columns: 1fr; } }
</style>
