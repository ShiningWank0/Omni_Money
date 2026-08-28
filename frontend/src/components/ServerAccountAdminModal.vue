<template>
  <div class="admin-overlay" @click.self="close">
    <section class="admin-card" role="dialog" aria-modal="true" aria-labelledby="server-admin-title">
      <header class="admin-header">
        <div>
          <h2 id="server-admin-title">サーバーユーザー管理</h2>
          <p>アカウント状態だけを管理します。ほかのユーザーの家計データへアクセスする機能はありません。</p>
        </div>
        <button type="button" class="icon-close" aria-label="閉じる" :disabled="busy" @click="close">×</button>
      </header>

      <div v-if="errorMessage" class="message error" role="alert">{{ errorMessage }}</div>
      <div v-if="infoMessage" class="message info" role="status">{{ infoMessage }}</div>

      <section class="admin-section" aria-labelledby="invite-title">
        <h3 id="invite-title">ユーザーを招待</h3>
        <form class="invite-form" @submit.prevent="createInvitation">
          <label>
            メールアドレス
            <input v-model.trim="inviteEmail" type="email" autocomplete="off" maxlength="254" :disabled="busy || Boolean(issuedToken)" required>
          </label>
          <label>
            権限
            <select v-model="inviteRole" :disabled="busy || Boolean(issuedToken)">
              <option value="user">一般ユーザー</option>
              <option value="admin">管理者</option>
            </select>
          </label>
          <button type="submit" :disabled="busy || Boolean(issuedToken)">招待を作成</button>
        </form>
      </section>

      <section v-if="issuedToken" class="token-panel" aria-live="polite">
        <h3>{{ issuedTokenKind === 'invite' ? '招待token' : 'パスワード再設定token' }}</h3>
        <p>
          このtokenは今だけ表示され、閉じると再取得できません。安全な経路で本人へ渡し、URLには含めないでください。
          Omni Moneyがクリップボードへ自動で書き込むことはありません。
        </p>
        <textarea :value="issuedToken" readonly rows="3" aria-label="一度だけ表示されるtoken" @focus="$event.target.select()"></textarea>
        <div class="token-actions">
          <button type="button" @click="copyIssuedToken">tokenをコピー</button>
          <button type="button" class="secondary" @click="confirmForgetIssuedToken">表示を消す</button>
        </div>
        <p class="completion-path">
          本人は <code>{{ issuedTokenKind === 'invite' ? '/login?mode=invite' : '/login?mode=reset' }}</code>
          を開き、tokenをフォームへ貼り付けます。
        </p>
      </section>

      <section class="admin-section users-section" aria-labelledby="users-title">
        <div class="section-title-row">
          <h3 id="users-title">ユーザー一覧</h3>
          <button type="button" class="secondary" :disabled="busy" @click="loadUsers">更新</button>
        </div>
        <div v-if="loadingUsers" class="loading" role="status">読み込み中...</div>
        <div v-else class="table-scroll">
          <table>
            <caption class="sr-only">サーバーユーザーのアカウント状態</caption>
            <thead>
              <tr><th scope="col">ユーザー</th><th scope="col">権限</th><th scope="col">状態</th><th scope="col">最終ログイン</th><th scope="col">操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="user in users" :key="user.id">
                <td><strong>{{ user.display_name }}</strong><span>{{ user.email }}</span></td>
                <td>{{ user.role === 'admin' ? '管理者' : '一般' }}</td>
                <td>{{ user.state === 'active' ? '有効' : '無効' }}</td>
                <td>{{ formatDate(user.last_login_at) }}</td>
                <td class="user-actions">
                  <button type="button" :disabled="busy || user.state !== 'active' || Boolean(issuedToken)" @click="startPasswordReset(user)">再設定token</button>
                  <button
                    type="button"
                    class="danger"
                    :disabled="busy || Boolean(issuedToken) || user.state !== 'active' || user.id === currentUserId"
                    @click="disableUser(user)"
                  >無効化</button>
                </td>
              </tr>
              <tr v-if="users.length === 0"><td colspan="5">ユーザーはいません</td></tr>
            </tbody>
          </table>
        </div>
      </section>
    </section>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import {
  createServerInvitation,
  createServerPasswordReset,
  disableServerUser,
  listServerUsers
} from '../utils/api'

const props = defineProps({ currentUserId: { type: String, default: '' } })
const emit = defineEmits(['close'])
const users = ref([])
const loadingUsers = ref(false)
const busy = ref(false)
const inviteEmail = ref('')
const inviteRole = ref('user')
const issuedToken = ref('')
const issuedTokenKind = ref('')
const errorMessage = ref('')
const infoMessage = ref('')

function forgetIssuedToken() {
  issuedToken.value = ''
  issuedTokenKind.value = ''
}

function confirmForgetIssuedToken() {
  if (!issuedToken.value || window.confirm('このtokenは再取得できません。表示を消しますか？')) {
    forgetIssuedToken()
  }
}

function clearTransientState() {
  forgetIssuedToken()
  inviteEmail.value = ''
  errorMessage.value = ''
  infoMessage.value = ''
}

function close() {
  if (busy.value) return
  if (issuedToken.value && !window.confirm('表示中のtokenは再取得できません。管理画面を閉じますか？')) return
  clearTransientState()
  emit('close')
}

onBeforeUnmount(clearTransientState)
onMounted(loadUsers)

async function loadUsers() {
  loadingUsers.value = true
  errorMessage.value = ''
  try {
    users.value = await listServerUsers()
  } catch (error) {
    errorMessage.value = error?.message || 'ユーザー一覧を取得できませんでした'
  } finally {
    loadingUsers.value = false
  }
}

async function createInvitation() {
  busy.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    const result = await createServerInvitation({ email: inviteEmail.value, role: inviteRole.value })
    issuedToken.value = result?.token || ''
    issuedTokenKind.value = 'invite'
    inviteEmail.value = ''
    if (!issuedToken.value) throw new Error('招待tokenを受け取れませんでした')
  } catch (error) {
    forgetIssuedToken()
    errorMessage.value = error?.message || '招待を作成できませんでした'
  } finally {
    busy.value = false
  }
}

async function startPasswordReset(user) {
  busy.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    const result = await createServerPasswordReset(user.id)
    issuedToken.value = result?.token || ''
    issuedTokenKind.value = 'reset'
    if (!issuedToken.value) throw new Error('再設定tokenを受け取れませんでした')
  } catch (error) {
    forgetIssuedToken()
    errorMessage.value = error?.message || 'パスワード再設定を開始できませんでした'
  } finally {
    busy.value = false
  }
}

async function disableUser(user) {
  if (!window.confirm(`${user.display_name} (${user.email}) を無効化しますか？`)) return
  busy.value = true
  errorMessage.value = ''
  infoMessage.value = ''
  try {
    await disableServerUser(user.id)
    infoMessage.value = `${user.display_name} を無効化しました`
    await loadUsers()
  } catch (error) {
    errorMessage.value = error?.message || 'ユーザーを無効化できませんでした'
  } finally {
    busy.value = false
  }
}

async function copyIssuedToken() {
  try {
    await navigator.clipboard.writeText(issuedToken.value)
    infoMessage.value = 'tokenをクリップボードへコピーしました。受け渡し後はクリップボードも消去してください'
  } catch {
    errorMessage.value = 'コピーできませんでした。tokenを選択して安全に受け渡してください'
  }
}

function formatDate(value) {
  if (!value) return '未ログイン'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '不明' : date.toLocaleString('ja-JP')
}
</script>

<style scoped>
.admin-overlay { position: fixed; inset: 0; z-index: 1200; display: flex; justify-content: center; align-items: center; padding: 1rem; background: rgba(0, 0, 0, 0.55); box-sizing: border-box; }
.admin-card { width: min(1040px, 100%); max-height: calc(100vh - 2rem); overflow: auto; padding: 1.5rem; border-radius: 18px; background: #fff; box-sizing: border-box; }
.admin-header, .section-title-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.admin-header h2, .admin-section h3, .token-panel h3 { margin: 0; }
.admin-header p { margin: 0.4rem 0 0; color: #555; line-height: 1.45; }
.icon-close { border: none; background: transparent; font-size: 1.8rem; cursor: pointer; }
.admin-section, .token-panel { margin-top: 1.25rem; padding: 1rem; border: 1px solid #dde1f6; border-radius: 12px; }
.invite-form { display: grid; grid-template-columns: minmax(220px, 1fr) 160px auto; align-items: end; gap: 0.75rem; margin-top: 0.75rem; }
label { display: grid; gap: 0.3rem; color: #333; font-size: 0.88rem; font-weight: 600; }
input, select, textarea { width: 100%; padding: 0.65rem; border: 1px solid #bbc2e7; border-radius: 8px; box-sizing: border-box; font: inherit; }
textarea { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; overflow-wrap: anywhere; resize: vertical; }
button { padding: 0.6rem 0.75rem; border: 1px solid #667eea; border-radius: 8px; color: #fff; background: #667eea; cursor: pointer; }
button:disabled { opacity: 0.5; cursor: not-allowed; }
button.secondary { color: #4d5fc7; background: #fff; }
button.danger { border-color: #c63232; background: #c63232; }
.token-panel { border-color: #e0ad3f; background: #fffaf0; }
.token-panel p { line-height: 1.45; }
.token-actions, .user-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-top: 0.6rem; }
.completion-path { margin-bottom: 0; }
.table-scroll { overflow-x: auto; }
table { width: 100%; margin-top: 0.75rem; border-collapse: collapse; font-size: 0.88rem; }
th, td { padding: 0.65rem; border-bottom: 1px solid #e5e7f2; text-align: left; vertical-align: middle; }
td span { display: block; color: #666; overflow-wrap: anywhere; }
.message { margin-top: 1rem; padding: 0.75rem; border-radius: 8px; }
.message.error { color: #a51d1d; background: #fff0f0; }
.message.info { color: #176b2c; background: #edfff1; }
.loading { margin-top: 0.75rem; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 760px) { .invite-form { grid-template-columns: 1fr; } .admin-card { padding: 1rem; } }
</style>
