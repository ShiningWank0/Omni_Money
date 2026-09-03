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
		  <label>新しいパスワード<input v-model="newPassword" type="password" autocomplete="new-password" maxlength="1024" required></label>
		  <label>新しいパスワード（確認）<input v-model="newPasswordConfirmation" type="password" autocomplete="new-password" maxlength="1024" required></label>
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
        <p>候補をこの端末で作成し、安全な場所へ保存したことを確認してから更新します。確認前は現在のコードが有効なままです。サーバーでは更新後に全端末からログアウトします。</p>
        <form @submit.prevent="rotateRecovery">
          <label>現在のパスワード<input v-model="recoveryPassword" type="password" autocomplete="current-password" maxlength="1024" required></label>
          <button type="submit" :disabled="busy || Boolean(recoveryCode)">新しい回復コードの候補を作成</button>
        </form>
      </section>

      <section v-if="recoveryCode" class="recovery-panel" aria-live="polite">
        <h3>新しい回復コード</h3>
        <p>まだアカウントには反映されていません。この画面を閉じると再表示できないため、先に安全な場所へ保存してください。</p>
        <textarea :value="recoveryCode" readonly rows="3" @focus="$event.target.select()"></textarea>
        <label class="check-row"><input v-model="recoverySaved" type="checkbox">安全な場所へ保存しました</label>
        <button type="button" :disabled="!canCommitRecovery" @click="finishRecovery">保存済みのコードへ更新</button>
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
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
	  changeDesktopVaultPassword, changeServerPassword, isWailsMode, listServerCredentials, logoutAll,
  rotateDesktopVaultRecovery, rotateServerRecoveryCode
} from '../utils/api'
import { destroySecretBytes, generateRecoverySecret, recoverySecretToCode } from '../utils/recovery'
import { canConfirmDesktopRecoveryDelivery, validateNewDesktopPassword } from '../utils/desktopVaultSafety'
import { validatePasswordBytes } from '../utils/passwordPolicy'

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
let pendingRecoverySecret = null
let serverRecoveryOutcomeUnknown = false
const canCommitRecovery = computed(() => canConfirmDesktopRecoveryDelivery({
  recoveryCode: recoveryCode.value,
  recoverySaved: recoverySaved.value,
  busy: busy.value
}))

function clearSecrets() {
  currentPassword.value = ''
  newPassword.value = ''
  newPasswordConfirmation.value = ''
  recoveryPassword.value = ''
  recoveryCode.value = ''
  recoverySaved.value = false
  destroySecretBytes(pendingRecoverySecret)
  pendingRecoverySecret = null
  serverRecoveryOutcomeUnknown = false
}

function close() {
  if (busy.value) return
  if (recoveryCode.value && !recoverySaved.value && !window.confirm('新しい回復コードを保存せずに閉じますか？')) return
  const mustSignOut = serverRecoveryOutcomeUnknown
  clearSecrets()
  if (mustSignOut) emit('signed-out', { outcomeUnknown: true })
  else emit('close')
}

async function changePassword() {
  errorMessage.value = ''
  infoMessage.value = ''
	let mutationAttempted = false
	try {
	  validatePasswordBytes(currentPassword.value)
	  validateNewDesktopPassword(newPassword.value, newPasswordConfirmation.value)
    if (currentPassword.value === newPassword.value) throw new Error('現在と異なるパスワードを指定してください')
    busy.value = true
    if (isWailsMode) {
      await changeDesktopVaultPassword(currentPassword.value, newPassword.value)
      infoMessage.value = 'パスワードを変更しました'
    } else {
	    mutationAttempted = true
      await changeServerPassword({ currentPassword: currentPassword.value, newPassword: newPassword.value, revokePasskeys: revokePasskeys.value })
      clearSecrets()
      emit('signed-out')
      return
    }
  } catch (error) {
	  if (!isWailsMode && mutationAttempted && error?.definitiveResponse !== true) {
	    clearSecrets()
	    emit('signed-out', { outcomeUnknown: true })
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

function rotateRecovery() {
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    validatePasswordBytes(recoveryPassword.value)
    destroySecretBytes(pendingRecoverySecret)
    pendingRecoverySecret = generateRecoverySecret()
    recoveryCode.value = recoverySecretToCode(pendingRecoverySecret)
    recoverySaved.value = false
    serverRecoveryOutcomeUnknown = false
  } catch (error) {
    clearSecrets()
    errorMessage.value = error?.message || '回復コードの候補を作成できませんでした'
  }
}

async function finishRecovery() {
  if (!canCommitRecovery.value || !pendingRecoverySecret) return
  busy.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    if (isWailsMode) {
      await rotateDesktopVaultRecovery(recoveryPassword.value, recoveryCode.value)
      clearSecrets()
      infoMessage.value = '回復コードを更新しました'
      return
    }
    await rotateServerRecoveryCode({ currentPassword: recoveryPassword.value, newRecoverySecret: pendingRecoverySecret })
    clearSecrets()
    emit('signed-out')
  } catch (error) {
    recoveryPassword.value = ''
    if (!isWailsMode && error?.definitiveResponse !== true) {
      serverRecoveryOutcomeUnknown = true
      emit('session-invalidated')
      errorMessage.value = '通信結果を確認できませんでした。保存済みの候補が反映された可能性があります。再ログイン後、この候補を回復コードとして保管してください。'
      return
    }
    if (isWailsMode) {
      errorMessage.value = `${error?.message || '更新結果を確認できませんでした'}。保存済みの同じ候補で再試行できます。更新されていなければ現在の回復コードが引き続き有効です。`
      return
    }
    errorMessage.value = error?.message || '回復コードを更新できませんでした'
  } finally {
    busy.value = false
  }
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
