<template>
  <div class="credential-overlay" @click.self="close">
    <section class="credential-card" role="dialog" aria-modal="true" aria-labelledby="credential-title">
      <header>
        <div>
          <h2 id="credential-title">認証情報の管理</h2>
          <p>Vaultの暗号鍵は変更せず、鍵を開くための認証情報だけを安全に更新します。</p>
        </div>
        <button type="button" class="icon-close" aria-label="閉じる" :disabled="busy" @click="close">×</button>
      </header>

      <div v-if="errorMessage" class="message error" role="alert">{{ errorMessage }}</div>
      <div v-if="infoMessage" class="message info" role="status">{{ infoMessage }}</div>
	  <section v-if="!isWailsMode && inventory" class="credential-section">
	    <h3>現在の認証情報</h3>
	    <ul class="inventory-list">
	      <li>パスワード: 更新 {{ formatDate(inventory.password?.updated_at) }}</li>
	      <li>回復コード: 更新 {{ formatDate(inventory.recovery?.created_at) }}</li>
	      <li>パスキー: {{ Array.isArray(inventory.passkeys) ? inventory.passkeys.length : 0 }}件（個別の詳細と失効は「パスキー設定」）</li>
	    </ul>
	  </section>

      <section class="credential-section">
        <h3>パスワードを変更</h3>
        <p v-if="!isWailsMode">変更後は全端末からログアウトします。登録済みパスキーは、下の選択に従って残すか一括失効します。</p>
        <form @submit.prevent="changePassword">
          <label>現在のパスワード<input v-model="currentPassword" type="password" autocomplete="current-password" maxlength="1024" required></label>
          <label>新しいパスワード<input v-model="newPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label>
          <label>新しいパスワード（確認）<input v-model="newPasswordConfirmation" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required></label>
          <label v-if="!isWailsMode" class="check-row">
            <input v-model="revokePasskeys" type="checkbox">
            登録済みパスキーもすべて失効する
          </label>
          <p v-if="!isWailsMode" class="policy-note">
            {{ revokePasskeys ? 'パスワードと全パスキーを更新対象にします。次回は新しいパスワードでログインしてください。' : 'パスキーは残るため、変更後も登録済みパスキーでログインできます。' }}
          </p>
          <button type="submit" :disabled="busy">パスワードを変更</button>
        </form>
      </section>

      <section class="credential-section">
        <h3>回復コードを更新</h3>
        <p>現在のコードは直ちに失効します。新しいコードを安全な場所へ保存してください。サーバーでは更新後に全端末からログアウトします。</p>
        <form @submit.prevent="rotateRecovery">
          <label>現在のパスワード<input v-model="recoveryPassword" type="password" autocomplete="current-password" maxlength="1024" required></label>
          <button type="submit" :disabled="busy || Boolean(recoveryCode)">新しい回復コードを発行</button>
        </form>
      </section>

      <section v-if="recoveryCode" class="recovery-panel" aria-live="polite">
        <h3>新しい回復コード</h3>
        <p>この画面を閉じると再表示できません。保存を確認するまで閉じないでください。</p>
        <textarea :value="recoveryCode" readonly rows="3" @focus="$event.target.select()"></textarea>
        <label class="check-row"><input v-model="recoverySaved" type="checkbox">安全な場所へ保存しました</label>
        <button type="button" :disabled="!recoverySaved" @click="finishRecovery">保存を確認</button>
      </section>

      <section v-if="!isWailsMode" class="credential-section">
        <h3>すべてのセッションを終了</h3>
        <p>この端末を含む全端末からログアウトします。パスワードとパスキー自体は変更しません。</p>
        <button type="button" class="danger" :disabled="busy" @click="endAllSessions">全端末からログアウト</button>
      </section>
    </section>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import {
	  changeDesktopVaultPassword, changeServerPassword, isWailsMode, listServerCredentials, logoutAll,
  rotateDesktopVaultRecovery, rotateServerRecoveryCode
} from '../utils/api'
import { generateRecoverySecret, recoverySecretToCode } from '../utils/recovery'
import { validateNewDesktopPassword } from '../utils/desktopVaultSafety'

const emit = defineEmits(['close', 'session-invalidated', 'signed-out'])
const busy = ref(false)
const currentPassword = ref('')
const newPassword = ref('')
const newPasswordConfirmation = ref('')
const recoveryPassword = ref('')
const revokePasskeys = ref(false)
const recoveryCode = ref('')
const recoverySaved = ref(false)
const errorMessage = ref('')
const infoMessage = ref('')
const inventory = ref(null)

function clearSecrets() {
  currentPassword.value = ''
  newPassword.value = ''
  newPasswordConfirmation.value = ''
  recoveryPassword.value = ''
  recoveryCode.value = ''
}

function close() {
  if (busy.value) return
  if (recoveryCode.value && !recoverySaved.value && !window.confirm('新しい回復コードを保存せずに閉じますか？')) return
	  const serverRecoveryPending = !isWailsMode && Boolean(recoveryCode.value)
  clearSecrets()
	  if (serverRecoveryPending) emit('signed-out')
	  else emit('close')
}

async function changePassword() {
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    validateNewDesktopPassword(newPassword.value, newPasswordConfirmation.value)
    if (currentPassword.value === newPassword.value) throw new Error('現在と異なるパスワードを指定してください')
    busy.value = true
    if (isWailsMode) {
      await changeDesktopVaultPassword(currentPassword.value, newPassword.value)
      infoMessage.value = 'パスワードを変更しました'
    } else {
      await changeServerPassword({ currentPassword: currentPassword.value, newPassword: newPassword.value, revokePasskeys: revokePasskeys.value })
      clearSecrets()
      emit('signed-out')
      return
    }
  } catch (error) {
	  if (!isWailsMode && error?.definitiveResponse !== true) {
	    clearSecrets()
	    emit('signed-out')
	    return
	  }
    errorMessage.value = error?.message || 'パスワードを変更できませんでした'
  } finally {
    currentPassword.value = ''
    newPassword.value = ''
    newPasswordConfirmation.value = ''
    busy.value = false
  }
}

async function rotateRecovery() {
  errorMessage.value = ''
  infoMessage.value = ''
  busy.value = true
  let secret
  try {
    if (isWailsMode) {
      const response = await rotateDesktopVaultRecovery(recoveryPassword.value)
      recoveryCode.value = response?.recovery_code || ''
    } else {
      secret = generateRecoverySecret()
      recoveryCode.value = recoverySecretToCode(secret)
      await rotateServerRecoveryCode({ currentPassword: recoveryPassword.value, newRecoverySecret: secret })
	  emit('session-invalidated')
    }
    if (!recoveryCode.value) throw new Error('新しい回復コードを受け取れませんでした')
  } catch (error) {
	    if (isWailsMode || error?.definitiveResponse) {
	      recoveryCode.value = ''
	      errorMessage.value = error?.message || '回復コードを更新できませんでした'
	    } else {
	      emit('session-invalidated')
	      errorMessage.value = '通信結果を確認できませんでした。更新が完了した可能性があるため、表示中の候補コードを保存してから再ログインし、回復コードを改めて更新してください。'
	    }
  } finally {
    if (secret) secret.fill(0)
    recoveryPassword.value = ''
    busy.value = false
  }
}

function finishRecovery() {
  recoveryCode.value = ''
  recoverySaved.value = false
  if (isWailsMode) infoMessage.value = '新しい回復コードの保存を確認しました'
  else emit('signed-out')
}

async function endAllSessions() {
  if (!window.confirm('この端末を含む全端末からログアウトしますか？')) return
  busy.value = true
  errorMessage.value = ''
  try {
    await logoutAll()
    emit('signed-out')
  } catch (error) {
    errorMessage.value = error?.message || '全セッションを終了できませんでした'
  } finally { busy.value = false }
}

onBeforeUnmount(clearSecrets)
onMounted(async () => {
  if (isWailsMode) return
  try { inventory.value = await listServerCredentials() }
  catch (error) { errorMessage.value = error?.message || '認証情報の一覧を取得できませんでした' }
})

function formatDate(value) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '不明' : date.toLocaleString('ja-JP')
}
</script>

<style scoped>
.credential-overlay { position: fixed; inset: 0; z-index: 1200; display: flex; justify-content: center; align-items: center; padding: 1rem; background: rgba(0,0,0,.55); box-sizing: border-box; }
.credential-card { width: min(760px,100%); max-height: calc(100vh - 2rem); overflow: auto; padding: 1.5rem; border-radius: 18px; background: #fff; box-sizing: border-box; }
header { display: flex; justify-content: space-between; gap: 1rem; } h2,h3 { margin: 0; } header p,.credential-section p { color: #555; line-height: 1.45; }
.icon-close { border: 0; color: #333; background: transparent; font-size: 1.8rem; }
.credential-section,.recovery-panel { margin-top: 1rem; padding: 1rem; border: 1px solid #dde1f6; border-radius: 12px; }
.recovery-panel { border-color: #e0ad3f; background: #fffaf0; }
form { display: grid; gap: .75rem; } label { display: grid; gap: .3rem; font-size: .88rem; font-weight: 600; }
input,textarea { width: 100%; padding: .65rem; border: 1px solid #bbc2e7; border-radius: 8px; box-sizing: border-box; font: inherit; }
textarea { font-family: ui-monospace,SFMono-Regular,Menlo,monospace; overflow-wrap: anywhere; }
.check-row { display: flex; align-items: center; gap: .5rem; } .check-row input { width: auto; }
button { padding: .6rem .75rem; border: 1px solid #667eea; border-radius: 8px; color: #fff; background: #667eea; cursor: pointer; } button:disabled { opacity: .5; cursor: not-allowed; }
button.danger { border-color: #c63232; background: #c63232; }.policy-note { margin: 0; font-size: .88rem; }
.message { margin-top: 1rem; padding: .75rem; border-radius: 8px; }.message.error { color:#a51d1d;background:#fff0f0; }.message.info { color:#176b2c;background:#edfff1; }
.inventory-list { margin:.75rem 0 0; padding-left:1.25rem; line-height:1.7; }
</style>
