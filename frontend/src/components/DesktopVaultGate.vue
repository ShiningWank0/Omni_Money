<template>
  <main class="desktop-vault-gate">
    <section class="desktop-vault-card" aria-labelledby="desktop-vault-title">
      <div class="desktop-vault-mark" aria-hidden="true">OM</div>
      <h1 id="desktop-vault-title">Omni Money</h1>
      <p class="desktop-vault-subtitle">暗号化されたローカル保管庫</p>

      <div v-if="loading" class="desktop-vault-status" role="status">保管庫の状態を確認しています…</div>

      <div v-else-if="fatalError" class="desktop-vault-error" role="alert">
        <strong>保管庫を安全に開けませんでした。</strong>
        <span>{{ fatalError }}</span>
        <span>データを上書きせず、アプリを終了してバックアップを確認してください。</span>
      </div>

      <div v-else-if="legacyMigrationRequired" class="desktop-vault-warning" role="alert">
        <strong>既存の平文データベースが見つかりました。</strong>
        <span>安全な移行処理が完了するまで、このデータベースは自動で開いたり上書きしたりしません。</span>
        <span>現在のファイルと snapshots フォルダーをバックアップしてから、移行対応版を使用してください。</span>
      </div>

      <div v-else-if="recoveryCode" class="desktop-vault-form" aria-live="polite">
        <h2>回復コードを保存してください</h2>
        <p>このコードは今だけ表示されます。パスワードを忘れた場合に必要で、Omni Money側では復元できません。</p>
        <label for="desktop-recovery-output">回復コード</label>
        <textarea id="desktop-recovery-output" :value="recoveryCode" readonly rows="3" spellcheck="false" @focus="$event.target.select()"></textarea>
        <button type="button" class="secondary-button" @click="copyRecoveryCode">{{ recoveryCopied ? 'コピーしました' : '回復コードをコピー' }}</button>
        <div v-if="operationError" class="desktop-vault-error" role="alert">{{ operationError }}</div>
        <label class="save-confirmation">
          <input v-model="recoverySaved" type="checkbox">
          <span>パスワード管理ツール等、Omni Moneyとは別の安全な場所に保存しました</span>
        </label>
        <button type="button" class="primary-button" :disabled="!recoverySaved" @click="finishSetup">保管庫を開く</button>
      </div>

      <form v-else-if="needsSetup" class="desktop-vault-form" @submit.prevent="setupVault">
        <h2>最初の管理者を作成</h2>
        <p>Desktop版は単一ユーザーです。このアカウントが管理者を兼ね、取引データはSQLCipherで暗号化されます。</p>
        <label for="desktop-new-password">パスワード（UTF-8で12 bytes以上）</label>
        <input id="desktop-new-password" v-model="password" type="password" autocomplete="new-password" maxlength="256" required autofocus>
        <label for="desktop-new-password-confirmation">パスワードの確認</label>
        <input id="desktop-new-password-confirmation" v-model="passwordConfirmation" type="password" autocomplete="new-password" maxlength="256" required>
        <div v-if="operationError" class="desktop-vault-error" role="alert">{{ operationError }}</div>
        <button class="primary-button" type="submit" :disabled="busy">{{ busy ? '暗号化保管庫を作成中…' : '管理者と保管庫を作成' }}</button>
      </form>

      <form v-else-if="recovering" class="desktop-vault-form" @submit.prevent="recoverVault">
        <h2>回復コードでパスワードを再設定</h2>
        <label for="desktop-recovery-code">回復コード</label>
        <textarea id="desktop-recovery-code" v-model.trim="enteredRecoveryCode" rows="3" autocomplete="off" spellcheck="false" required></textarea>
        <label for="desktop-recovery-password">新しいパスワード（UTF-8で12 bytes以上）</label>
        <input id="desktop-recovery-password" v-model="password" type="password" autocomplete="new-password" maxlength="256" required>
        <label for="desktop-recovery-password-confirmation">新しいパスワードの確認</label>
        <input id="desktop-recovery-password-confirmation" v-model="passwordConfirmation" type="password" autocomplete="new-password" maxlength="256" required>
        <div v-if="operationError" class="desktop-vault-error" role="alert">{{ operationError }}</div>
        <div class="button-row">
          <button type="button" class="secondary-button" :disabled="busy" @click="cancelRecovery">戻る</button>
          <button type="submit" class="primary-button" :disabled="busy">{{ busy ? '確認中…' : '再設定して開く' }}</button>
        </div>
      </form>

      <form v-else class="desktop-vault-form" @submit.prevent="unlockVault">
        <h2>保管庫を開く</h2>
        <label for="desktop-password">パスワード</label>
        <input id="desktop-password" v-model="password" type="password" autocomplete="current-password" maxlength="256" required autofocus>
        <div v-if="operationError" class="desktop-vault-error" role="alert">{{ operationError }}</div>
        <button class="primary-button" type="submit" :disabled="busy">{{ busy ? '確認中…' : 'ロックを解除' }}</button>
        <button class="link-button" type="button" :disabled="busy" @click="startRecovery">パスワードを忘れた場合</button>
      </form>
    </section>
  </main>
</template>

<script setup>
import { computed, ref } from 'vue'
import { recoverDesktopVault, setupDesktopVault, unlockDesktopVault } from '../utils/api'
import { desktopVaultNeedsLegacyMigration, desktopVaultNeedsSetup, validateNewDesktopPassword } from '../utils/desktopVaultSafety'

const props = defineProps({
  status: { type: Object, default: null },
  loading: { type: Boolean, default: false },
  fatalError: { type: String, default: '' }
})
const emit = defineEmits(['unlocked'])

const busy = ref(false)
const operationError = ref('')
const password = ref('')
const passwordConfirmation = ref('')
const recovering = ref(false)
const enteredRecoveryCode = ref('')
const recoveryCode = ref('')
const recoverySaved = ref(false)
const recoveryCopied = ref(false)

const needsSetup = computed(() => desktopVaultNeedsSetup(props.status))
const legacyMigrationRequired = computed(() => desktopVaultNeedsLegacyMigration(props.status))

function validateNewPassword() {
  validateNewDesktopPassword(password.value, passwordConfirmation.value)
}

function clearCredentials() {
  password.value = ''
  passwordConfirmation.value = ''
  enteredRecoveryCode.value = ''
}

async function setupVault() {
  if (busy.value) return
  operationError.value = ''
  try {
    validateNewPassword()
    busy.value = true
    const result = await setupDesktopVault(password.value)
    recoveryCode.value = result?.recovery_code || ''
    if (!recoveryCode.value) throw new Error('回復コードを受信できなかったため、保管庫を開けません')
  } catch (error) {
    operationError.value = error?.message || '暗号化保管庫を作成できませんでした'
  } finally {
    clearCredentials()
    busy.value = false
  }
}

async function unlockVault() {
  if (busy.value) return
  operationError.value = ''
  busy.value = true
  try {
    const status = await unlockDesktopVault(password.value)
    emit('unlocked', status)
  } catch (error) {
    operationError.value = error?.message || 'パスワードを確認できませんでした'
  } finally {
    clearCredentials()
    busy.value = false
  }
}

async function recoverVault() {
  if (busy.value) return
  operationError.value = ''
  try {
    validateNewPassword()
    busy.value = true
    const result = await recoverDesktopVault(enteredRecoveryCode.value, password.value)
    recoveryCode.value = result?.recovery_code || ''
    if (!recoveryCode.value) throw new Error('新しい回復コードを受信できなかったため、保管庫を開けません')
    recovering.value = false
  } catch (error) {
    operationError.value = error?.message || '回復コードを確認できませんでした'
  } finally {
    clearCredentials()
    busy.value = false
  }
}

function startRecovery() {
  operationError.value = ''
  clearCredentials()
  recovering.value = true
}

function cancelRecovery() {
  operationError.value = ''
  clearCredentials()
  recovering.value = false
}

async function copyRecoveryCode() {
  try {
    await navigator.clipboard.writeText(recoveryCode.value)
    recoveryCopied.value = true
  } catch {
    operationError.value = 'コピーできませんでした。回復コードを選択して手動で保存してください'
  }
}

function finishSetup() {
  if (!recoverySaved.value) return
  const status = { ...(props.status || {}), state: 'unlocked', configured: true, unlocked: true, role: 'admin' }
  recoveryCode.value = ''
  recoverySaved.value = false
  recoveryCopied.value = false
  emit('unlocked', status)
}
</script>

<style scoped>
.desktop-vault-gate {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 24px;
  background: linear-gradient(145deg, #eef1ff 0%, #f9faff 50%, #f3f5fa 100%);
}

.desktop-vault-card {
  width: min(100%, 480px);
  padding: 32px;
  border: 1px solid #d8def7;
  border-radius: 20px;
  background: rgba(255, 255, 255, 0.98);
  box-shadow: 0 24px 70px rgba(48, 58, 112, 0.16);
}

.desktop-vault-mark {
  width: 52px;
  height: 52px;
  display: grid;
  place-items: center;
  margin: 0 auto 12px;
  border-radius: 15px;
  color: white;
  background: #5f6fd8;
  font-weight: 800;
  letter-spacing: 0.04em;
}

h1, h2 { margin: 0; text-align: center; color: #252a44; }
h1 { font-size: 1.65rem; }
h2 { margin-bottom: 10px; font-size: 1.15rem; }
.desktop-vault-subtitle { margin: 6px 0 26px; text-align: center; color: #66708f; }
.desktop-vault-form { display: grid; gap: 12px; }
.desktop-vault-form p { margin: 0 0 4px; color: #5b6277; line-height: 1.55; }
label { color: #353b54; font-size: 0.92rem; font-weight: 650; }
input:not([type='checkbox']), textarea {
  box-sizing: border-box;
  width: 100%;
  padding: 11px 12px;
  border: 1px solid #bcc4df;
  border-radius: 9px;
  color: #252a44;
  background: white;
  font: inherit;
}
textarea { resize: vertical; word-break: break-all; }
input:focus, textarea:focus { outline: 3px solid rgba(95, 111, 216, 0.2); border-color: #5f6fd8; }
button { min-height: 42px; border-radius: 9px; font: inherit; font-weight: 700; cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.5; }
.primary-button { border: 0; color: white; background: #5f6fd8; }
.secondary-button { border: 1px solid #aeb7d7; color: #343a55; background: #f7f8fd; }
.link-button { border: 0; color: #4d5cbd; background: transparent; }
.button-row { display: grid; grid-template-columns: 1fr 1.5fr; gap: 10px; }
.save-confirmation { display: flex; align-items: flex-start; gap: 9px; padding: 10px; border-radius: 9px; background: #f4f6ff; line-height: 1.45; }
.save-confirmation input { margin-top: 3px; }
.desktop-vault-status { text-align: center; color: #545e7e; }
.desktop-vault-error, .desktop-vault-warning { display: grid; gap: 8px; padding: 12px; border-radius: 9px; line-height: 1.45; }
.desktop-vault-error { color: #7b222b; background: #fff1f2; border: 1px solid #e5abb0; }
.desktop-vault-warning { color: #684b11; background: #fff8df; border: 1px solid #e8ce76; }

@media (max-width: 540px) {
  .desktop-vault-gate { padding: 12px; }
  .desktop-vault-card { padding: 24px 18px; border-radius: 15px; }
}
</style>
