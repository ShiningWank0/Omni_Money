<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <section class="modal-content transaction-details" role="dialog" aria-modal="true" aria-labelledby="transaction-details-title">
      <header class="details-header">
        <h3 id="transaction-details-title">取引の詳細</h3>
        <div class="details-actions">
          <button ref="editButton" type="button" class="details-icon-button" title="取引を編集" aria-label="取引を編集" @click="$emit('edit')">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L9 17l-4 1 1-4L16.5 3.5Z" />
            </svg>
          </button>
          <button type="button" class="details-icon-button" title="閉じる" aria-label="閉じる" @click="$emit('close')">×</button>
        </div>
      </header>

      <div class="details-body">
        <dl class="details-fields">
          <div><dt>日付・時刻</dt><dd>{{ transaction.date || '—' }}</dd></div>
          <div><dt>資金項目</dt><dd>{{ transaction.fundItem || transaction.account || '—' }}</dd></div>
          <div><dt>種類</dt><dd>{{ transaction.type === 'income' ? '収入' : '支出' }}</dd></div>
          <div><dt>項目</dt><dd>{{ transaction.item || '—' }}</dd></div>
          <div><dt>金額</dt><dd :class="transaction.type === 'income' ? 'income-cell' : 'expense-cell'">{{ transaction.type === 'income' ? '+' : '-' }}{{ formatExactCurrency(transaction.amount, transaction.amount_exact) }}</dd></div>
          <div><dt>メモ</dt><dd class="details-memo">{{ transaction.memo || '—' }}</dd></div>
          <div><dt>タグ</dt><dd>{{ transaction.tags?.length ? transaction.tags.map(tag => tag.name).join('、') : 'なし' }}</dd></div>
        </dl>

        <section class="details-images" :aria-busy="imagesLoading" aria-label="添付画像">
          <h4>画像</h4>
          <p v-if="imagesLoading" role="status">画像を読み込んでいます…</p>
          <div v-else-if="imagesError" role="alert">
            <p>画像を読み込めませんでした。</p>
            <button type="button" class="details-retry" @click="loadImages">再読み込み</button>
          </div>
          <p v-else-if="images.length === 0">添付画像はありません</p>
          <div v-else class="details-image-list">
            <figure v-for="image in images" :key="image.id">
              <img v-if="imageURL(image)" :src="imageURL(image)" :alt="image.filename || '添付画像'">
              <div v-else class="details-invalid-image">この画像は表示できません</div>
              <figcaption>
                <span>{{ image.filename || '添付画像' }}</span>
                <button type="button" class="details-image-remove" :aria-label="`${image.filename || '添付画像'}を削除`" @click="$emit('remove-image', image.id)">削除</button>
              </figcaption>
            </figure>
          </div>
        </section>
      </div>
    </section>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { getTransactionImages } from '../utils/api'
import { formatExactCurrency } from '../utils/exactAmount'

const props = defineProps({ transaction: { type: Object, required: true } })
const emit = defineEmits(['edit', 'close', 'remove-image'])
const editButton = ref(null)
const images = ref([])
const imagesLoading = ref(false)
const imagesError = ref(false)
let active = true

function imageURL(image) {
  if (image.invalid || typeof image.data_url !== 'string') return ''
  return /^data:image\/(?:jpeg|png|gif|webp);base64,[A-Za-z0-9+/=]+$/.test(image.data_url)
    ? image.data_url
    : ''
}

async function loadImages() {
  imagesLoading.value = true
  imagesError.value = false
  try {
    const result = await getTransactionImages(props.transaction.id)
    if (active) images.value = result
  } catch {
    if (active) imagesError.value = true
  } finally {
    if (active) imagesLoading.value = false
  }
}

function onKeydown(event) {
  if (event.key === 'Escape') emit('close')
}

onMounted(() => {
  editButton.value?.focus()
  window.addEventListener('keydown', onKeydown)
  loadImages()
})
onBeforeUnmount(() => {
  active = false
  window.removeEventListener('keydown', onKeydown)
})
</script>

<style scoped>
.modal-content.transaction-details { width: min(1000px, calc(100vw - 3rem)); max-width: 1000px; max-height: calc(100dvh - 3rem); overflow: hidden; }
.details-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-bottom: 1rem; }
.details-header h3 { margin: 0; }
.details-actions { display: flex; gap: .4rem; }
.details-icon-button { display: grid; place-items: center; width: 2.5rem; height: 2.5rem; border: 1px solid #c9d0e5; border-radius: .6rem; background: #fff; color: #344274; cursor: pointer; font-size: 1.5rem; }
.details-icon-button:hover, .details-icon-button:focus-visible { border-color: #667eea; background: #f1f3ff; }
.details-icon-button svg { width: 1.2rem; height: 1.2rem; }
.details-body { overflow-y: auto; min-height: 0; }
.details-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: 1.5rem; margin: 0; }
.details-fields > div { min-width: 0; padding: .6rem 0; border-bottom: 1px solid #e8e8e8; }
.details-fields dt { color: #5e6664; }
.details-fields dd { margin: .2rem 0 0; overflow-wrap: anywhere; }
.details-memo { white-space: pre-wrap; }
.details-images { margin-top: 1rem; }
.details-images h4 { margin: 0 0 .75rem; }
.details-images p { margin: 0 0 .75rem; }
.details-image-list { display: grid; gap: 1rem; }
.details-image-list figure { margin: 0; padding: .5rem; border: 1px solid #ddd; border-radius: .6rem; text-align: center; }
.details-image-list img { display: block; max-width: 100%; max-height: 55vh; margin: auto; object-fit: contain; }
.details-image-list figcaption { display: flex; align-items: center; justify-content: space-between; gap: .75rem; margin-top: .4rem; overflow-wrap: anywhere; font-size: .85rem; color: #565d5b; }
.details-image-remove { flex-shrink: 0; border: 1px solid #db8f8f; border-radius: .5rem; padding: .35rem .65rem; background: #fff; color: #982626; cursor: pointer; }
.details-image-remove:hover { background: #fff0f0; }
.details-invalid-image { padding: 1rem; color: #8a2525; }
.details-retry { padding: .4rem .8rem; border: 1px solid #667eea; border-radius: .5rem; background: #fff; color: #4358b4; cursor: pointer; }
@media (max-width: 700px) {
  .modal-content.transaction-details { width: calc(100vw - 1rem); max-height: calc(100dvh - 1rem); padding: 1rem; }
  .details-fields { grid-template-columns: minmax(0, 1fr); }
  .details-image-list figcaption { flex-wrap: wrap; }
}
</style>
