<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <section class="modal-content transaction-details" role="dialog" aria-modal="true"
      aria-labelledby="transaction-details-title" :inert="Boolean(lightboxImage)"
      :aria-hidden="lightboxImage ? 'true' : undefined">
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
        <div class="details-overview">
          <p class="details-item">{{ transaction.item || '—' }}</p>
          <p class="details-amount" :class="transaction.type === 'income' ? 'income-cell' : 'expense-cell'">
            {{ transaction.type === 'income' ? '+' : '-' }}{{ formatExactCurrency(transaction.amount, transaction.amount_exact) }}
          </p>
        </div>
        <dl class="details-fields">
          <div><dt>日付・時刻</dt><dd>{{ transaction.date || '—' }}</dd></div>
          <div><dt>資金項目</dt><dd>{{ transaction.fundItem || transaction.account || '—' }}</dd></div>
          <div><dt>種類</dt><dd>{{ transaction.type === 'income' ? '収入' : '支出' }}</dd></div>
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
            <figure>
              <button v-if="imageURL(selectedImage)" ref="imageOpenButton" type="button" class="details-image-open"
                :aria-label="`${selectedImage.filename || '添付画像'}を拡大表示`" @click="openLightbox">
                <img :src="imageURL(selectedImage)" :alt="selectedImage.filename || '添付画像'">
              </button>
              <div v-else class="details-invalid-image">この画像は表示できません</div>
              <figcaption>
                <span>{{ selectedImage.filename || '添付画像' }}</span>
                <button type="button" class="details-image-remove" :aria-label="`${selectedImage.filename || '添付画像'}を削除`" @click="$emit('remove-image', selectedImage.id)">削除</button>
              </figcaption>
            </figure>
            <div v-if="images.length > 1" class="details-image-thumbnails" aria-label="画像の選択">
              <button v-for="(image, index) in images" :key="image.id" type="button"
                class="details-image-thumbnail" :class="{ selected: index === selectedImageIndex }"
                :aria-label="`画像${index + 1}: ${image.filename || '添付画像'}`"
                :aria-pressed="index === selectedImageIndex" @click="selectedImageIndex = index">
                <img v-if="imageURL(image)" :src="imageURL(image)" alt="">
                <span v-else>表示不可</span>
              </button>
            </div>
          </div>
        </section>
      </div>
    </section>
    <Teleport to="body">
      <div v-if="lightboxImage" class="details-lightbox" role="dialog" aria-modal="true"
        aria-label="画像の拡大表示" @click.self="closeLightbox">
        <div class="details-lightbox-actions">
          <button type="button" @click="lightboxZoomed = !lightboxZoomed">
            {{ lightboxZoomed ? '全体表示' : '原寸表示' }}
          </button>
          <button ref="lightboxCloseButton" type="button" aria-label="画像を閉じる" @click="closeLightbox">×</button>
        </div>
        <img :src="imageURL(lightboxImage)" :alt="lightboxImage.filename || '添付画像'"
          :class="{ 'is-zoomed': lightboxZoomed }">
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { getTransactionImages } from '../utils/api'
import { formatExactCurrency } from '../utils/exactAmount'

const props = defineProps({ transaction: { type: Object, required: true } })
const emit = defineEmits(['edit', 'close', 'remove-image'])
const editButton = ref(null)
const images = ref([])
const imagesLoading = ref(false)
const imagesError = ref(false)
const selectedImageIndex = ref(0)
const selectedImage = computed(() => images.value[selectedImageIndex.value] || images.value[0] || null)
const imageOpenButton = ref(null)
const lightboxImage = ref(null)
const lightboxZoomed = ref(false)
const lightboxCloseButton = ref(null)
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
    if (active) {
      images.value = result
      selectedImageIndex.value = 0
    }
  } catch {
    if (active) imagesError.value = true
  } finally {
    if (active) imagesLoading.value = false
  }
}

async function openLightbox() {
  if (!selectedImage.value || !imageURL(selectedImage.value)) return
  imageOpenButton.value?.blur()
  lightboxImage.value = selectedImage.value
  lightboxZoomed.value = false
  await nextTick()
  lightboxCloseButton.value?.focus()
}

async function closeLightbox() {
  lightboxImage.value = null
  lightboxZoomed.value = false
  await nextTick()
  if (active) imageOpenButton.value?.focus()
}

function onKeydown(event) {
  if (event.key !== 'Escape') return
  if (lightboxImage.value) {
    closeLightbox()
  } else {
    emit('close')
  }
}

onMounted(() => {
  editButton.value?.focus()
  window.addEventListener('keydown', onKeydown)
  loadImages()
})
onBeforeUnmount(() => {
  active = false
  lightboxImage.value = null
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
.details-overview, .details-fields { width: min(100%, 760px); margin-inline: auto; }
.details-overview { padding: .2rem 0 1rem; border-bottom: 1px solid #e8e8e8; }
.details-item { margin: 0 0 .35rem; font-size: 1.35rem; font-weight: 650; overflow-wrap: anywhere; }
.details-amount { margin: 0; font-size: 1.8rem; font-weight: 700; }
.details-fields { margin-top: 0; margin-bottom: 0; }
.details-fields > div { display: grid; grid-template-columns: 8rem minmax(0, 1fr); gap: 1rem; min-width: 0; padding: .7rem 0; border-bottom: 1px solid #e8e8e8; }
.details-fields dt { color: #5e6664; }
.details-fields dd { margin: 0; overflow-wrap: anywhere; }
.details-memo { white-space: pre-wrap; }
.details-images { margin-top: 1.5rem; }
.details-images h4 { margin: 0 0 .75rem; }
.details-images p { margin: 0 0 .75rem; }
.details-image-list figure { margin: 0; padding: .5rem; border: 1px solid #ddd; border-radius: .6rem; text-align: center; }
.details-image-open { display: block; width: 100%; border: 0; padding: 0; background: transparent; cursor: zoom-in; }
.details-image-open img { display: block; max-width: 100%; max-height: 55vh; margin: auto; object-fit: contain; }
.details-image-list figcaption { display: flex; align-items: center; justify-content: space-between; gap: .75rem; margin-top: .4rem; overflow-wrap: anywhere; font-size: .85rem; color: #565d5b; }
.details-image-remove { flex-shrink: 0; border: 1px solid #db8f8f; border-radius: .5rem; padding: .35rem .65rem; background: #fff; color: #982626; cursor: pointer; }
.details-image-remove:hover { background: #fff0f0; }
.details-image-thumbnails { display: flex; gap: .5rem; margin-top: .7rem; padding-bottom: .3rem; overflow-x: auto; }
.details-image-thumbnail { flex: 0 0 76px; height: 76px; padding: .25rem; border: 2px solid #d6dbea; border-radius: .55rem; background: #fff; cursor: pointer; }
.details-image-thumbnail.selected { border-color: #667eea; }
.details-image-thumbnail img { width: 100%; height: 100%; object-fit: cover; }
.details-image-thumbnail span { font-size: .7rem; }
.details-invalid-image { padding: 1rem; color: #8a2525; }
.details-retry { padding: .4rem .8rem; border: 1px solid #667eea; border-radius: .5rem; background: #fff; color: #4358b4; cursor: pointer; }
.details-lightbox { position: fixed; inset: 0; z-index: 1100; display: flex; align-items: center; justify-content: center; overflow: auto; padding: 1.5rem; box-sizing: border-box; background: rgba(10, 15, 22, .92); }
.details-lightbox img { display: block; flex: none; max-width: 100%; max-height: 100%; width: auto; height: auto; margin: auto; object-fit: contain; }
.details-lightbox img.is-zoomed { max-width: none; max-height: none; }
.details-lightbox-actions { position: fixed; top: .75rem; right: .75rem; z-index: 1; display: flex; gap: .5rem; }
.details-lightbox-actions button { min-height: 2.5rem; border: 1px solid #fff; border-radius: .5rem; padding: .35rem .8rem; background: rgba(10, 15, 22, .85); color: #fff; font: inherit; cursor: pointer; }
.details-lightbox-actions button:last-child { min-width: 2.5rem; font-size: 1.5rem; line-height: 1; }
@media (max-width: 700px) {
  .modal-content.transaction-details { width: calc(100vw - 1rem); max-height: calc(100dvh - 1rem); padding: 1rem; }
  .details-fields > div { grid-template-columns: minmax(0, 1fr); gap: .2rem; }
  .details-image-list figcaption { flex-wrap: wrap; }
  .details-lightbox { padding: .5rem; }
}
</style>
