<template>
  <div class="modal-overlay" @click.self="close">
    <section ref="dialog" class="modal-content subscription-manager" role="dialog" aria-modal="true" aria-labelledby="subscription-title" @keydown.esc.stop="close" @keydown.tab="trapFocus">
      <header class="subscription-header">
        <h3 id="subscription-title">サブスク管理</h3>
        <PanelCloseButton label="サブスク管理を閉じる" :disabled="busy" @click="close" />
      </header>
      <p v-if="error" class="subscription-error" role="alert">{{ error }}</p>
      <p v-if="loading" role="status">読み込んでいます…</p>

      <form v-if="form" class="subscription-form" @submit.prevent="save">
        <h4>{{ editingID ? 'サブスクを編集' : 'サブスクを登録' }}</h4>
        <div class="subscription-fields">
          <label>項目名<input v-model="form.item" required maxlength="200"></label>
          <label>資金項目<input v-model="form.account" required maxlength="100"></label>
          <label>周期<select v-model="form.interval" @change="alignDate"><option value="monthly">月額</option><option value="yearly">年額</option></select></label>
          <label>通貨<select v-model="form.currency" @change="form.original_amount = ''"><option v-for="currency in subscriptionCurrencies" :key="currency">{{ currency }}</option></select></label>
          <label>金額<input v-model="form.original_amount" inputmode="decimal" required :pattern="form.currency === 'JPY' ? '[1-9][0-9]{0,11}' : '(0|[1-9][0-9]{0,11})(\\.[0-9]{1,2})?'" placeholder="0.00"></label>
          <label>基準日<input v-model.number="form.anchor_day" type="number" min="1" max="31" required @change="alignDate"></label>
          <label v-if="form.interval === 'yearly'">基準月<input v-model.number="form.anchor_month" type="number" min="1" max="12" required @change="alignDate"></label>
          <label>次回支払日<input v-model="form.next_date" type="date" min="0001-01-01" max="9998-12-31" required></label>
          <label>タイムゾーン<input v-model="form.timezone" required maxlength="64" list="subscription-timezones"></label>
          <datalist id="subscription-timezones"><option>Asia/Tokyo</option><option>UTC</option><option>America/New_York</option><option>Europe/London</option></datalist>
          <label class="subscription-wide">メモ<textarea v-model="form.memo" maxlength="10000" rows="2"></textarea></label>
          <label v-if="tags.length" class="subscription-wide">タグ<select v-model="form.tags" multiple><option v-for="tag in tags" :key="tag.id" :value="tag.id">{{ tag.path }}</option></select></label>
          <label class="subscription-wide subscription-check"><input v-model="form.enabled" type="checkbox">自動登録を有効にする</label>
        </div>
        <p>次回支払日から適用します。元の取引と、すでに作成した取引は変更しません。短い月は月末に寄せます。</p>
        <p v-if="form.currency !== 'JPY'">Frankfurter経由のECB日次参照レートで円換算し、1円単位で四捨五入します。休日は直近7日以内の公表値を使います。実際の請求額は取引を編集して調整できます。取得先に送信するのは通貨と日付だけです。</p>
        <p v-if="form.currency !== 'JPY'">未来の支払いのレートは確定しません。レートを取得できない場合は自動登録を保留し、再試行します。</p>
        <div class="subscription-actions"><button type="button" :disabled="busy" @click="cancelForm">キャンセル</button><button type="submit" :disabled="busy">{{ busy ? '保存しています…' : '保存' }}</button></div>
      </form>

      <template v-else>
        <nav class="subscription-tabs" aria-label="サブスクの表示">
          <button v-for="option in tabs" :key="option.id" type="button" :aria-pressed="tab === option.id" @click="selectTab(option.id)">{{ option.label }}</button>
        </nav>
        <div v-if="tab === 'rules'" class="subscription-rule-list">
          <p v-if="!loading && !rules.length">取引の詳細からサブスクを登録できます。</p>
          <article v-for="rule in rules" :key="rule.id" class="subscription-rule">
            <div><h4>{{ rule.item }}</h4><p>{{ rule.interval === 'monthly' ? '月額' : '年額' }} {{ rule.original_amount }} {{ rule.currency }} ・ {{ rule.account }}</p><p>{{ rule.next_date }} ・ {{ rule.enabled ? '有効' : '停止' }} ・ {{ rule.timezone }}</p></div>
            <div class="subscription-actions"><button type="button" :disabled="busy" @click="edit(rule)">編集</button><button type="button" :disabled="busy" @click="toggle(rule)">{{ rule.enabled ? '停止' : '再開' }}</button><button type="button" @click="showHistory(rule)">取引を表示</button></div>
          </article>
        </div>

        <div v-if="tab === 'calendar'">
          <div class="subscription-month-navigation">
            <button type="button" aria-label="前月" :disabled="month === '0001-01'" @click="moveMonth(-1)">‹</button>
            <label>表示月<input v-model="month" type="month" min="0001-01" max="9998-12" required @change="loadCalendar"></label>
            <button type="button" aria-label="翌月" :disabled="month === '9998-12'" @click="moveMonth(1)">›</button>
          </div>
          <div class="subscription-calendar" aria-label="支払日カレンダー">
            <div v-for="day in weekdays" :key="day" class="subscription-weekday">{{ day }}</div>
            <div v-for="(date, index) in cells" :key="date || `empty-${index}`" class="subscription-day" :class="{ 'subscription-empty': !date }">
              <span v-if="date" class="subscription-day-number">{{ Number(date.slice(-2)) }}</span>
              <button v-for="(entry, entryIndex) in entries.filter(entry => entry.date === date)" :key="`${entry.rule_id}-${entryIndex}`" type="button" class="subscription-calendar-payment" :class="{ 'subscription-faded': entry.transaction?.subscription?.unconfirmed }" @click="openEntry(entry)">
                <span>{{ entry.item }}</span><span :class="entry.transaction ? 'expense-cell' : ''">{{ entry.transaction ? formatExactCurrency(entry.transaction.amount, entry.transaction.amount_exact) : `${entry.original_amount} ${entry.currency}` }}</span>
              </button>
            </div>
          </div>
        </div>

        <div v-if="tab === 'candidates'">
          <p>繰り返しの支出から見つけた候補です。内容を確認して保存すると自動登録を開始します。</p>
          <p v-if="!loading && !candidates.length">現在の候補はありません。</p>
          <article v-for="candidate in candidates" :key="candidate.fingerprint" class="subscription-rule">
            <div><h4>{{ candidate.transaction.item }}</h4><p>{{ candidate.interval === 'monthly' ? '月額' : '年額' }} {{ formatExactCurrency(candidate.transaction.amount, candidate.transaction.amount_exact) }} ・ {{ candidate.transaction.account }}</p></div>
            <div class="subscription-actions"><button type="button" :disabled="busy" @click="create(candidate.transaction, candidate.interval)">内容を確認</button><button type="button" :disabled="busy" @click="dismiss(candidate)">今後表示しない</button></div>
          </article>
        </div>

        <div v-if="tab === 'history' && historyRule">
          <h4>{{ historyRule.item }} の取引</h4>
          <form class="subscription-history-filters" @submit.prevent="loadHistory(0)">
            <label>資金項目<input v-model="filters.account"></label><label>項目名・メモ<input v-model="filters.search" maxlength="1000"></label>
            <label>開始日<input v-model="filters.start" type="date"></label><label>終了日<input v-model="filters.end" type="date"></label><button type="submit">検索</button>
          </form>
          <p v-if="!loading && !history.length">条件に一致する取引はありません。</p>
          <button v-for="transaction in history" :key="transaction.id" type="button" class="subscription-history-payment" :class="{ 'subscription-faded': transaction.subscription?.unconfirmed }" @click="emit('open-transaction', transaction)">
            <span>{{ transaction.date }} ・ {{ transaction.account }} ・ {{ transaction.item }}</span><span class="expense-cell">{{ formatExactCurrency(transaction.amount, transaction.amount_exact) }}</span>
          </button>
          <div class="subscription-actions"><button type="button" :disabled="historyOffset === 0 || loading" @click="loadHistory(Math.max(0, historyOffset - 100))">前へ</button><button type="button" :disabled="nextOffset < 0 || loading" @click="loadHistory(nextOffset)">次へ</button></div>
        </div>
        <div class="subscription-actions subscription-footer"><button type="button" :disabled="loading || busy" @click="reload">再読み込み</button></div>
      </template>
    </section>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import PanelCloseButton from './PanelCloseButton.vue'
import { getSubscriptions, saveSubscription, getSubscriptionCalendar, getSubscriptionCandidates, dismissSubscriptionCandidate, getSubscriptionTransactions, getTags } from '../utils/api'
import { calendarCells, initialSubscription, localDate, nextSubscriptionDate, scheduleDate, subscriptionCurrencies } from '../utils/subscriptions'
import { formatExactCurrency } from '../utils/exactAmount'

const props = defineProps({ sourceTransaction: { type: Object, default: null } })
const emit = defineEmits(['close', 'changed', 'open-transaction'])
const dialog = ref(null), rules = ref([]), tags = ref([]), candidates = ref([]), entries = ref([]), history = ref([])
const loading = ref(false), busy = ref(false), error = ref(''), form = ref(null), editingID = ref(''), tab = ref('rules')
const month = ref(localDate().slice(0, 7)), historyRule = ref(null), historyOffset = ref(0), nextOffset = ref(-1)
const filters = ref({ account: '', search: '', start: '', end: '' })
const cells = computed(() => calendarCells(month.value))
const tabs = [{ id: 'rules', label: '一覧' }, { id: 'calendar', label: 'カレンダー' }, { id: 'candidates', label: '候補' }]
const weekdays = ['日', '月', '火', '水', '木', '金', '土']
let mounted = true, requestGeneration = 0
const previousFocus = document.activeElement

function close() { if (!busy.value) emit('close') }
function trapFocus(event) {
  const focusable = [...dialog.value.querySelectorAll('button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled)')].filter(element => element.getClientRects().length)
  const first = focusable[0], last = focusable.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
function flattenTags(values, prefix = '') {
  return values.flatMap(tag => { const path = prefix ? `${prefix} / ${tag.name}` : tag.name; return [{ ...tag, path }, ...flattenTags(tag.children || [], path)] })
}
async function read(work, apply) {
  const generation = ++requestGeneration
  loading.value = true; error.value = ''
  try { const value = await work(); if (mounted && generation === requestGeneration) apply(value) }
  catch (cause) { if (mounted && generation === requestGeneration) error.value = cause.message || '読み込めませんでした' }
  finally { if (mounted && generation === requestGeneration) loading.value = false }
}
async function reload() {
  if (tab.value === 'calendar') return loadCalendar()
  if (tab.value === 'candidates') return read(getSubscriptionCandidates, value => { candidates.value = value })
  if (tab.value === 'history') return loadHistory(historyOffset.value)
  return read(getSubscriptions, value => { rules.value = value })
}
function selectTab(value) { tab.value = value; void reload() }
function create(transaction, interval = 'monthly') { editingID.value = ''; form.value = initialSubscription(transaction, interval); error.value = '' }
function edit(rule) { editingID.value = rule.id; const { id, ...input } = rule; form.value = { ...input, tags: [...rule.tags] }; error.value = '' }
function cancelForm() { form.value = null; error.value = ''; if (props.sourceTransaction) emit('close') }
function alignDate() {
  const [year, month] = form.value.next_date.split('-').map(Number)
  if (year && month && form.value.anchor_day >= 1 && form.value.anchor_day <= 31) form.value.next_date = scheduleDate(year, form.value.interval === 'yearly' ? form.value.anchor_month : month, form.value.anchor_day)
}
async function save() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    await saveSubscription(editingID.value, { ...form.value, tags: [...form.value.tags] })
    if (!mounted) return
    form.value = null; tab.value = 'rules'; emit('changed'); await reload()
  } catch (cause) { if (mounted) error.value = cause.message || '保存できませんでした' }
  finally { if (mounted) busy.value = false }
}
async function toggle(rule) {
  if (!rule.enabled) {
    edit(rule); form.value.enabled = true
    const today = localDate()
    if (form.value.next_date < today) {
      const [year, month] = today.split('-').map(Number)
      form.value.next_date = scheduleDate(year, rule.interval === 'yearly' ? rule.anchor_month : month, rule.anchor_day)
      if (form.value.next_date < today) form.value.next_date = nextSubscriptionDate(form.value.next_date, rule.interval, rule.anchor_day, rule.anchor_month)
    }
    return
  }
  busy.value = true; error.value = ''
  try {
    const { id, ...input } = rule
    await saveSubscription(id, { ...input, enabled: false })
    if (!mounted) return
    emit('changed'); await reload()
  } catch (cause) { if (mounted) error.value = cause.message || '停止できませんでした' }
  finally { if (mounted) busy.value = false }
}
function loadCalendar() {
  if (!calendarCells(month.value).length) { entries.value = []; error.value = '表示月を指定してください'; return }
  entries.value = []
  return read(() => getSubscriptionCalendar(month.value), value => { entries.value = value })
}
function moveMonth(delta) {
  let [year, number] = month.value.split('-').map(Number)
  number += delta; if (number < 1) { number = 12; year-- } if (number > 12) { number = 1; year++ }
  if (year < 1 || year > 9998) return
  month.value = `${String(year).padStart(4, '0')}-${String(number).padStart(2, '0')}`; void loadCalendar()
}
function openEntry(entry) {
  if (entry.transaction) { emit('open-transaction', entry.transaction); return }
  const rule = rules.value.find(rule => rule.id === entry.rule_id)
  if (rule) edit(rule)
  else error.value = '一覧を再読み込みしてください'
}
function showHistory(rule) { historyRule.value = rule; tab.value = 'history'; filters.value = { account: '', search: '', start: '', end: '' }; void loadHistory(0) }
function loadHistory(offset) {
  history.value = []; historyOffset.value = offset
  return read(() => getSubscriptionTransactions(historyRule.value.id, { ...filters.value, offset }), value => { history.value = value.transactions; nextOffset.value = value.next_offset })
}
async function dismiss(candidate) {
  busy.value = true
  try { await dismissSubscriptionCandidate(candidate.fingerprint); if (mounted) candidates.value = candidates.value.filter(value => value.fingerprint !== candidate.fingerprint) }
  catch (cause) { if (mounted) error.value = cause.message }
  finally { if (mounted) busy.value = false }
}
onMounted(async () => {
  await read(async () => Promise.all([getSubscriptions(), getTags()]), ([values, allTags]) => { rules.value = values; tags.value = flattenTags(allTags) })
  if (!mounted) return
  if (props.sourceTransaction) create(props.sourceTransaction)
  await nextTick(); dialog.value?.querySelector('button')?.focus()
})
onBeforeUnmount(() => { mounted = false; requestGeneration++; previousFocus?.focus?.() })
</script>

<style scoped>
.subscription-manager { width: min(960px, 95vw); max-width: 960px; max-height: 90vh; overflow-y: auto; padding: 1.5rem; color: var(--omni-text, #17352c); }
.subscription-manager button:not(.panel-close-button) { border: 1px solid var(--omni-border, #dce9e2); border-radius: 8px; padding: .6rem .8rem; background: var(--omni-surface-muted, #f5faf7); color: inherit; cursor: pointer; }
.subscription-manager button:disabled { opacity: .55; cursor: default; }
.subscription-manager button[type="submit"] { background: var(--omni-primary, #167a62); color: #fff; }
.subscription-manager input, .subscription-manager select, .subscription-manager textarea { border: 1px solid var(--omni-border-strong, #c8ddd2); border-radius: 8px; font: inherit; color: inherit; background: #fff; }
.subscription-header, .subscription-actions, .subscription-month-navigation, .subscription-rule { display: flex; align-items: center; gap: .75rem; }
.subscription-header { justify-content: space-between; margin-bottom: 1rem; }
.subscription-header h3, .subscription-rule h4 { margin: 0; }
.subscription-tabs { display: flex; flex-wrap: wrap; gap: .5rem; margin: 1rem 0; }
.subscription-tabs button[aria-pressed="true"] { box-shadow: inset 0 -2px currentColor; }
.subscription-rule { justify-content: space-between; border-bottom: 1px solid var(--omni-border, #dce9e2); padding: 1rem 0; }
.subscription-rule p, .subscription-form p { font-size: .85rem; line-height: 1.6; overflow-wrap: anywhere; }
.subscription-actions { flex-wrap: wrap; justify-content: flex-end; }
.subscription-fields, .subscription-history-filters { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
.subscription-fields label, .subscription-history-filters label, .subscription-month-navigation label { display: grid; gap: .4rem; }
.subscription-fields input, .subscription-fields select, .subscription-fields textarea, .subscription-history-filters input { box-sizing: border-box; width: 100%; min-width: 0; padding: .65rem; }
.subscription-wide { grid-column: 1 / -1; }
.subscription-check { display: flex !important; align-items: center; }
.subscription-check input { width: auto; }
.subscription-calendar { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); margin-top: 1rem; }
.subscription-weekday { text-align: center; padding: .6rem .1rem; font-size: .8rem; }
.subscription-day { min-height: 90px; min-width: 0; border: 1px solid var(--omni-border, #dce9e2); padding: .25rem; }
.subscription-empty { opacity: .25; }
.subscription-day-number { font-size: .75rem; }
.subscription-manager .subscription-calendar-payment { display: grid; gap: .25rem; width: 100%; text-align: left; font-size: .75rem; padding: .3rem; margin-top: .3rem; overflow-wrap: anywhere; }
.subscription-history-payment { display: flex; justify-content: space-between; gap: .5rem; width: 100%; text-align: left; margin: .5rem 0; padding: .75rem; overflow-wrap: anywhere; }
.subscription-faded { opacity: .55; }
.subscription-error { color: var(--omni-expense, #b42318); }
.subscription-footer { margin-top: 1rem; }
@media (max-width: 640px) {
  .subscription-manager { padding: .8rem; }
  .subscription-rule { align-items: flex-start; flex-direction: column; }
  .subscription-fields, .subscription-history-filters { grid-template-columns: minmax(0, 1fr); }
  .subscription-day { min-height: 75px; padding: .1rem; }
  .subscription-manager .subscription-calendar-payment { font-size: .75rem; padding: .15rem; }
  .subscription-history-payment { flex-direction: column; }
}
</style>
