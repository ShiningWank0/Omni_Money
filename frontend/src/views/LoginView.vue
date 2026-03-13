<template>
  <div class="login-root">
    <div class="login-container">
      <div class="login-card">
        <div class="login-header">
          <h1 class="app-title">Omni Money</h1>
          <p class="app-subtitle">サーバーモードにログイン</p>
        </div>

        <form @submit.prevent="handleLogin">
          <div v-if="errorMessage" class="error-message">{{ errorMessage }}</div>
          <div v-if="remainingAttempts !== null && remainingAttempts >= 0" class="attempts-warning">
            残り {{ remainingAttempts }} 回でロックされます
          </div>

          <div class="form-group">
            <label for="password" class="form-label">パスワード</label>
            <input
              id="password"
              v-model="password"
              type="password"
              class="form-input"
              autocomplete="current-password"
              required
            >
          </div>

          <button type="submit" class="login-button" :disabled="loading">
            <span v-if="loading" class="loading-spinner"></span>
            <span>{{ loading ? 'ログイン中...' : 'ログイン' }}</span>
          </button>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { getAuthStatus, isWailsMode, login } from '../utils/api'

const password = ref('')
const loading = ref(false)
const errorMessage = ref('')
const remainingAttempts = ref(null)

onMounted(async () => {
  if (isWailsMode) {
    window.location.href = '/'
    return
  }

  try {
    const status = await getAuthStatus()
    if (status?.authenticated) {
      window.location.href = '/'
    }
  } catch (error) {
    // 認証未完了時はログイン画面を表示し続ける
  }
})

async function handleLogin() {
  loading.value = true
  errorMessage.value = ''
  remainingAttempts.value = null

  try {
    await login(password.value)
    window.location.href = '/'
  } catch (error) {
    errorMessage.value = error?.message || 'ログインに失敗しました'
    if (typeof error?.remainingAttempts === 'number') {
      remainingAttempts.value = error.remainingAttempts
    }
  } finally {
    loading.value = false
    password.value = ''
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

.attempts-warning {
  background: rgba(255, 149, 0, 0.1);
  border: 1px solid rgba(255, 149, 0, 0.3);
  color: #bf5700;
  padding: 0.5rem 1rem;
  border-radius: 8px;
  margin-bottom: 1rem;
  font-size: 0.85rem;
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
