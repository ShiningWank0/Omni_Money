<template>
  <div class="passkey-overlay" @click.self="close">
    <section class="passkey-card" role="dialog" aria-modal="true" aria-labelledby="passkey-settings-title">
      <header class="passkey-header">
        <div>
          <h2 id="passkey-settings-title">パスキー設定</h2>
          <p>パスワードに加えて、端末の生体認証や画面ロックでログインできます。</p>
        </div>
        <button type="button" class="icon-close" aria-label="閉じる" :disabled="busy" @click="close">×</button>
      </header>

      <div v-if="errorMessage" class="message error" role="alert">{{ errorMessage }}</div>
      <div v-if="infoMessage" class="message info" role="status">{{ infoMessage }}</div>
      <div v-if="!supported" class="message warning" role="status">
        パスキーの登録には、HTTPS接続と対応ブラウザが必要です。
      </div>

      <section class="passkey-section" aria-labelledby="register-passkey-title">
        <h3 id="register-passkey-title">新しいパスキーを登録</h3>
        <p class="section-description">
          Vault鍵をこのパスキーでも安全に開けるようにするため、現在のパスワードを一度だけ確認します。
        </p>
        <form class="registration-form" @submit.prevent="register">
          <label>
            パスキー名
            <input v-model.trim="name" type="text" autocomplete="off" maxlength="120" placeholder="例: MacBook Touch ID" :disabled="busy || !supported" required>
          </label>
          <label>
            現在のパスワード
			<input v-model="password" type="password" autocomplete="current-password" maxlength="1024" :disabled="busy || !supported" required>
          </label>
          <button type="submit" :disabled="busy || !supported">{{ registering ? '登録中...' : 'パスキーを登録' }}</button>
        </form>
      </section>

      <section class="passkey-section" aria-labelledby="registered-passkeys-title">
        <div class="section-title-row">
          <h3 id="registered-passkeys-title">登録済みパスキー</h3>
          <button type="button" class="secondary" :disabled="busy" @click="load">更新</button>
        </div>
        <div v-if="loading" class="loading" role="status">読み込み中...</div>
        <ul v-else-if="passkeys.length" class="passkey-list">
          <li v-for="passkey in passkeys" :key="passkey.id">
            <div>
              <strong>{{ passkey.name }}</strong>
              <span>登録: {{ formatDate(passkey.created_at) }}</span>
              <span>最終利用: {{ passkey.last_used_at ? formatDate(passkey.last_used_at) : '未使用' }}</span>
            </div>
            <button type="button" class="danger" :disabled="busy" @click="remove(passkey)">削除</button>
          </li>
        </ul>
        <p v-else class="empty-state">登録済みのパスキーはありません。パスワードでのログインは引き続き利用できます。</p>
		<div v-if="passkeys.length" class="bulk-revoke">
		  <p>一括失効すると、この端末を含む全端末からログアウトします。パスワードでのログインは残ります。</p>
		  <button type="button" class="danger" :disabled="busy" @click="removeAll">すべてのパスキーを失効</button>
		</div>
      </section>
    </section>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { deleteAllPasskeys, deletePasskey, listPasskeys, registerPasskey } from '../utils/api'
import { passkeysSupported } from '../utils/passkeys'
import { validatePasswordBytes } from '../utils/passwordPolicy'

const emit = defineEmits(['close'])
const passkeys = ref([])
const name = ref('')
const password = ref('')
const loading = ref(false)
const registering = ref(false)
const deleting = ref(false)
const errorMessage = ref('')
const infoMessage = ref('')
const supported = passkeysSupported()
const busy = ref(false)

function clearPassword() {
  password.value = ''
}

function close() {
  if (busy.value) return
  clearPassword()
  emit('close')
}

onMounted(load)
onBeforeUnmount(clearPassword)

async function load() {
  loading.value = true
  busy.value = true
  errorMessage.value = ''
  try {
    passkeys.value = await listPasskeys()
  } catch (error) {
    errorMessage.value = error?.message || 'パスキー一覧を取得できませんでした'
  } finally {
    loading.value = false
    busy.value = registering.value || deleting.value
  }
}

async function register() {
  registering.value = true
  busy.value = true
  errorMessage.value = ''
	infoMessage.value = ''
	try {
	  validatePasswordBytes(password.value)
	  await registerPasskey({ name: name.value, password: password.value })
    name.value = ''
    infoMessage.value = 'パスキーを登録しました。次回からパスワードまたはパスキーでログインできます'
    passkeys.value = await listPasskeys()
  } catch (error) {
    errorMessage.value = error?.message || 'パスキーを登録できませんでした'
  } finally {
    clearPassword()
    registering.value = false
    busy.value = deleting.value
  }
}

async function remove(passkey) {
	  if (!window.confirm(`「${passkey.name}」を失効しますか？ 全端末からログアウトしますが、パスワードでのログインは引き続き利用できます。`)) return
  deleting.value = true
  busy.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    await deletePasskey(passkey.id)
	    window.location.replace('/login')
  } catch (error) {
    errorMessage.value = error?.message || 'パスキーを削除できませんでした'
  } finally {
    deleting.value = false
    busy.value = registering.value
  }
}

async function removeAll() {
  if (!window.confirm('登録済みパスキーをすべて失効し、全端末からログアウトしますか？ パスワードは変更されません。')) return
  deleting.value = true; busy.value = true; errorMessage.value = ''; infoMessage.value = ''
  try { await deleteAllPasskeys(); window.location.replace('/login') }
  catch (error) { errorMessage.value = error?.message || 'パスキーを一括失効できませんでした' }
  finally { deleting.value = false; busy.value = registering.value }
}

function formatDate(value) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '不明' : date.toLocaleString('ja-JP')
}
</script>

<style scoped>
.passkey-overlay { position: fixed; inset: 0; z-index: 1200; display: flex; justify-content: center; align-items: center; padding: 1rem; background: rgba(0, 0, 0, 0.55); box-sizing: border-box; }
.passkey-card { width: min(720px, 100%); max-height: calc(100vh - 2rem); overflow: auto; padding: 1.5rem; border-radius: 18px; background: #fff; box-sizing: border-box; }
.passkey-header, .section-title-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.passkey-header h2, .passkey-section h3 { margin: 0; }
.passkey-header p, .section-description { margin: 0.4rem 0 0; color: #555; line-height: 1.45; }
.icon-close { border: none; color: #333; background: transparent; font-size: 1.8rem; cursor: pointer; }
.passkey-section { margin-top: 1.25rem; padding: 1rem; border: 1px solid #dde1f6; border-radius: 12px; }
.registration-form { display: grid; gap: 0.8rem; margin-top: 0.9rem; }
label { display: grid; gap: 0.3rem; color: #333; font-size: 0.88rem; font-weight: 600; }
input { width: 100%; padding: 0.65rem; border: 1px solid #bbc2e7; border-radius: 8px; box-sizing: border-box; font: inherit; }
button { padding: 0.6rem 0.75rem; border: 1px solid #667eea; border-radius: 8px; color: #fff; background: #667eea; cursor: pointer; }
button:disabled { opacity: 0.5; cursor: not-allowed; }
button.secondary { color: #4d5fc7; background: #fff; }
button.danger { border-color: #c63232; background: #c63232; }
.passkey-list { display: grid; gap: 0.7rem; margin: 0.9rem 0 0; padding: 0; list-style: none; }
.passkey-list li { display: flex; justify-content: space-between; align-items: center; gap: 1rem; padding: 0.8rem; border: 1px solid #e5e7f2; border-radius: 9px; }
.passkey-list strong, .passkey-list span { display: block; }
.passkey-list span { margin-top: 0.2rem; color: #666; font-size: 0.82rem; }
.message { margin-top: 1rem; padding: 0.75rem; border-radius: 8px; }
.message.error { color: #a51d1d; background: #fff0f0; }
.message.info { color: #176b2c; background: #edfff1; }
.message.warning { color: #694d00; background: #fff8e8; }
.loading, .empty-state { margin: 0.9rem 0 0; color: #555; }
.bulk-revoke { margin-top:1rem; padding-top:1rem; border-top:1px solid #e5e7f2; }.bulk-revoke p { color:#555; line-height:1.45; }
@media (max-width: 560px) { .passkey-card { padding: 1rem; } .passkey-list li { align-items: flex-start; flex-direction: column; } }
</style>
