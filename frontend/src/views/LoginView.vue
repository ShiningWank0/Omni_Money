<template>
  <div class="login-root">
    <div class="login-container">
      <div class="login-card">
        <div class="login-header">
          <h1 class="app-title">Omni Money</h1>
          <p class="app-subtitle">{{ setupRequired ? '最初の管理者を作成' : 'サーバーモードにログイン' }}</p>
        </div>

        <form @submit.prevent="handleSubmit">
          <div v-if="errorMessage" class="error-message">{{ errorMessage }}</div>
          <div v-if="infoMessage" class="info-message">{{ infoMessage }}</div>
          <div v-if="setupRequired" class="form-group">
            <label for="setup-token" class="form-label">初期設定トークン</label>
            <input
              id="setup-token"
              v-model="setupToken"
              type="password"
              class="form-input"
              autocomplete="off"
              maxlength="512"
              required
            >
          </div>

          <div class="form-group">
            <label for="email" class="form-label">メールアドレス</label>
            <input
              id="email"
              v-model.trim="email"
              type="email"
              class="form-input"
              autocomplete="username"
              maxlength="254"
              required
            >
          </div>

          <div v-if="setupRequired" class="form-group">
            <label for="display-name" class="form-label">表示名</label>
            <input
              id="display-name"
              v-model.trim="displayName"
              type="text"
              class="form-input"
              autocomplete="name"
              maxlength="120"
              required
            >
          </div>

          <div class="form-group">
            <label for="password" class="form-label">パスワード</label>
            <input
              id="password"
              v-model="password"
              type="password"
              class="form-input"
              autocomplete="current-password"
              minlength="12"
              maxlength="256"
              required
            >
          </div>

          <div v-if="setupRequired" class="form-group">
            <label for="password-confirmation" class="form-label">パスワード（確認）</label>
            <input
              id="password-confirmation"
              v-model="passwordConfirmation"
              type="password"
              class="form-input"
              autocomplete="new-password"
              minlength="12"
              maxlength="256"
              required
            >
          </div>

          <div v-if="setupRequired" class="recovery-panel">
            <div class="form-label">回復コード</div>
            <p class="recovery-warning">
              パスワードを忘れた場合に既存データを開く唯一のコードです。管理者でも復元できません。
              パスワードマネージャー等の安全な場所へ保存してください。
            </p>
            <textarea
              class="recovery-code"
              :value="recoveryCode"
              readonly
              rows="3"
              aria-label="回復コード"
              @focus="$event.target.select()"
            ></textarea>
            <button type="button" class="secondary-button" @click="copyRecoveryCode">
              回復コードをコピー
            </button>
            <label class="confirmation-label">
              <input v-model="recoverySaved" type="checkbox" required>
              安全な場所へ保存しました
            </label>
          </div>

          <button type="submit" class="login-button" :disabled="loading">
            <span v-if="loading" class="loading-spinner"></span>
            <span>{{ submitLabel }}</span>
          </button>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { getAuthStatus, isWailsMode, login, setupInitialAdmin } from '../utils/api'

const email = ref('')
const password = ref('')
const passwordConfirmation = ref('')
const displayName = ref('')
const setupToken = ref('')
const setupRequired = ref(false)
const recoveryBytes = ref(null)
const recoveryCode = ref('')
const recoverySaved = ref(false)
const loading = ref(false)
const errorMessage = ref('')
const infoMessage = ref('')

const submitLabel = computed(() => {
  if (loading.value) return setupRequired.value ? '作成中...' : 'ログイン中...'
  return setupRequired.value ? '管理者を作成してログイン' : 'ログイン'
})

function bytesToBase64URL(bytes) {
  let binary = ''
  for (const value of bytes) binary += String.fromCharCode(value)
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

function prepareRecoveryCode() {
  const bytes = window.crypto.getRandomValues(new Uint8Array(32))
  recoveryBytes.value = bytes
  recoveryCode.value = bytesToBase64URL(bytes)
  recoverySaved.value = false
}

onMounted(async () => {
  if (isWailsMode) {
    window.location.href = '/'
    return
  }

  try {
    const status = await getAuthStatus()
    if (status?.authenticated) {
      window.location.href = '/'
      return
    }
    setupRequired.value = Boolean(status?.setup_required)
    if (setupRequired.value) {
      prepareRecoveryCode()
    }
  } catch (error) {
    // 認証未完了時はログイン画面を表示し続ける
  }
})

async function copyRecoveryCode() {
  try {
    await navigator.clipboard.writeText(recoveryCode.value)
    infoMessage.value = '回復コードをクリップボードへコピーしました'
  } catch {
    errorMessage.value = 'コピーできませんでした。回復コードを選択して保存してください'
  }
}

async function handleSubmit() {
  loading.value = true
  errorMessage.value = ''
  infoMessage.value = ''

  try {
    if (setupRequired.value) {
      if (password.value !== passwordConfirmation.value) {
        throw new Error('確認用パスワードが一致しません')
      }
      if (!recoverySaved.value || !recoveryBytes.value) {
        throw new Error('回復コードを安全な場所へ保存してください')
      }
      await setupInitialAdmin({
        setupToken: setupToken.value,
        email: email.value,
        displayName: displayName.value,
        password: password.value,
        recoverySecret: recoveryBytes.value
      })
      recoveryBytes.value.fill(0)
      recoveryBytes.value = null
      recoveryCode.value = ''
      setupRequired.value = false
    }
    await login(email.value, password.value)
    window.location.href = '/'
  } catch (error) {
    errorMessage.value = error?.message || 'ログインに失敗しました'
  } finally {
    loading.value = false
    password.value = ''
    passwordConfirmation.value = ''
    setupToken.value = ''
  }
}
</script>

<style scoped>
.login-root {
  width: 100%;
  min-height: calc(100vh - 2rem);
  display: flex;
  align-items: center;
  justify-content: center;
}

.login-container {
  width: 100%;
  max-width: 400px;
  padding: 1rem;
}

.login-card {
  background: rgba(255, 255, 255, 0.9);
  border-radius: 20px;
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.2);
  backdrop-filter: blur(10px);
  padding: 2rem;
  box-sizing: border-box;
}

.login-header {
  text-align: center;
  margin-bottom: 1.5rem;
}

.app-title {
  font-size: 2rem;
  font-weight: 700;
  margin: 0 0 0.4rem;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}

.app-subtitle {
  margin: 0;
  color: #666;
}

.form-group {
  margin-bottom: 1rem;
}

.form-label {
  display: block;
  margin-bottom: 0.4rem;
  color: #333;
  font-size: 0.9rem;
  font-weight: 600;
}

.form-input {
  width: 100%;
  padding: 0.75rem 1rem;
  border: 2px solid rgba(102, 126, 234, 0.2);
  border-radius: 10px;
  box-sizing: border-box;
}

.form-input:focus {
  outline: none;
  border-color: #667eea;
  box-shadow: 0 0 20px rgba(102, 126, 234, 0.2);
}

.login-button {
  width: 100%;
  padding: 0.75rem;
  border: none;
  border-radius: 10px;
  color: #fff;
  font-size: 1rem;
  font-weight: 600;
  cursor: pointer;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
}

.login-button:disabled {
  opacity: 0.65;
  cursor: not-allowed;
}

.error-message {
  background: rgba(255, 69, 58, 0.1);
  border: 1px solid rgba(255, 69, 58, 0.3);
  color: #d70015;
  padding: 0.75rem 1rem;
  border-radius: 10px;
  margin-bottom: 1rem;
  font-size: 0.9rem;
}

.info-message {
  background: rgba(52, 199, 89, 0.1);
  border: 1px solid rgba(52, 199, 89, 0.3);
  color: #176b2c;
  padding: 0.75rem 1rem;
  border-radius: 10px;
  margin-bottom: 1rem;
  font-size: 0.9rem;
}

.recovery-panel {
  margin: 1rem 0;
  padding: 1rem;
  border: 1px solid rgba(102, 126, 234, 0.25);
  border-radius: 10px;
  background: rgba(102, 126, 234, 0.06);
}

.recovery-warning {
  margin: 0 0 0.75rem;
  color: #4a4a4a;
  font-size: 0.86rem;
  line-height: 1.45;
}

.recovery-code {
  width: 100%;
  box-sizing: border-box;
  resize: none;
  overflow-wrap: anywhere;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.secondary-button {
  margin-top: 0.5rem;
  padding: 0.5rem 0.75rem;
  border: 1px solid #667eea;
  border-radius: 8px;
  color: #4d5fc7;
  background: #fff;
  cursor: pointer;
}

.confirmation-label {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin-top: 0.85rem;
  font-size: 0.9rem;
  color: #333;
}

.loading-spinner {
  display: inline-block;
  margin-right: 0.5rem;
  width: 14px;
  height: 14px;
  border: 2px solid rgba(255, 255, 255, 0.3);
  border-top: 2px solid #fff;
  border-radius: 50%;
  animation: spin 1s linear infinite;
  vertical-align: text-bottom;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
