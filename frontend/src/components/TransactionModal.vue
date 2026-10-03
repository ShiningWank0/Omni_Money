<template>
  <div class="modal-overlay" @click="!busy && $emit('close')">
    <div class="modal-content transaction-modal" @click.stop>
      <h3>{{ isEditMode ? '取引を編集' : '新しい取引を追加' }}</h3>
      <form @submit.prevent="handleSubmit" :aria-busy="busy">
        <fieldset :disabled="busy" :inert="busy" class="transaction-fields">
        <div class="form-container">
          <div class="transaction-primary-fields">
          <div class="form-row">
            <label>日付:</label>
            <input type="date" v-model="form.date" required>
          </div>
          <div class="form-row">
            <label>時刻 (任意):</label>
            <input type="time" v-model="form.time">
          </div>
          <div class="form-row">
            <label>資金項目:</label>
            <div class="funditem-input-group" @click.stop>
              <input type="text"
                v-model="form.fundItem"
                placeholder="資金項目名を入力または選択"
                required
                @focus="showFundItemDropdown = true">
              <button type="button" class="dropdown-toggle-btn" @click="showFundItemDropdown = !showFundItemDropdown">▼</button>
              <div v-if="showFundItemDropdown" class="funditem-dropdown">
                <ul>
                  <li v-for="item in fundItems" :key="item"
                    @click="form.fundItem = item; showFundItemDropdown = false"
                    :class="{ 'selected': item === form.fundItem }">
                    {{ item }}
                  </li>
                </ul>
              </div>
            </div>
            <small v-if="isNewFundItem" class="new-account-notice">新しい資金項目「{{ form.fundItem }}」が作成されます</small>
          </div>
          <div class="form-row">
            <label>種類:</label>
            <div class="radio-group">
              <label><input type="radio" v-model="form.type" value="income"> 収入</label>
              <label><input type="radio" v-model="form.type" value="expense"> 支出</label>
            </div>
          </div>
          <div class="form-row">
            <label>項目:</label>
            <div class="item-input-group">
              <input type="text" v-model="form.item" placeholder="例: 給与、食費、交通費" required list="item-list">
              <datalist id="item-list">
                <option v-for="item in itemNames" :key="item" :value="item"></option>
              </datalist>
            </div>
            <small v-if="isNewItem" class="new-account-notice">新しい項目「{{ form.item }}」が作成されます</small>
          </div>
          <div class="form-row">
            <label>金額:</label>
            <input type="text" v-model="form.amount" placeholder="円" required
              :class="form.type === 'income' ? 'amount-input-income' : 'amount-input-expense'"
              @input="onAmountInput"
              inputmode="numeric"
              aria-describedby="amount-limit-hint"
              autocomplete="off">
            <small id="amount-limit-hint">1取引あたり10億円以下</small>
          </div>
          <div class="form-row">
            <label>メモ (任意):</label>
            <input type="text" v-model="form.memo" placeholder="メモを入力">
          </div>

          <!-- タグ選択 (Agent.md §6.6) -->
          <div class="form-row">
            <label>タグ:</label>
            <div class="tag-selector">
              <div class="selected-tags">
                <span v-for="tag in selectedTags" :key="tag.id" class="tag-badge">
                  {{ getTagPath(tag) }}<small v-if="tag.pending">（保存時に作成）</small>
                  <button type="button" class="tag-remove" @click="removeTag(tag.id)">×</button>
                </span>
              </div>
              <div class="tag-dropdown-group">
                <select v-model="selectedLevel1" @change="onLevel1Change" class="tag-select">
                  <option value="">タグを選択...</option>
                  <option v-for="t in level1Tags" :key="t.id" :value="t.id">{{ t.name }}</option>
                </select>
                <select v-if="level2Tags.length > 0" v-model="selectedLevel2" @change="onLevel2Change" class="tag-select">
                  <option value="">サブタグ...</option>
                  <option v-for="t in level2Tags" :key="t.id" :value="t.id">{{ t.name }}</option>
                </select>
                <select v-if="level3Tags.length > 0" v-model="selectedLevel3" class="tag-select">
                  <option value="">サブサブタグ...</option>
                  <option v-for="t in level3Tags" :key="t.id" :value="t.id">{{ t.name }}</option>
                </select>
                <button type="button" class="add-tag-btn" @click="addSelectedTag">追加</button>
              </div>
              <div class="new-tag-row">
                <input type="text" v-model="newTagName" placeholder="新規タグ名（/で階層作成）" class="new-tag-input">
                <button type="button" class="add-tag-btn" @click="createNewTag">作成</button>
              </div>
            </div>
          </div>

          <!-- 取引紐付け (Agent.md §6.2) - カード支払いと銀行引き落としのみ -->
          <div v-if="showLinkSection" class="form-row">
            <label>紐付け:</label>
            <div class="link-section">
              <div class="link-hint">{{ linkHint }}</div>
              <div v-if="linkedTransactions.length > 0" class="linked-list">
                <div v-for="lt in linkedTransactions" :key="lt.id" class="linked-item">
                  <div class="linked-info">
                    <span class="linked-date">{{ lt.date }}</span>
                    <span class="linked-account">{{ lt.fundItem }}</span>
                    <span class="linked-item-name">{{ lt.item }}</span>
                    <span :class="lt.type === 'income' ? 'linked-amount-income' : 'linked-amount-expense'">
                      {{ lt.type === 'income' ? '+' : '-' }}¥{{ formatExactInteger(lt.amount, lt.amount_exact) }}
                    </span>
                  </div>
                  <button type="button" class="link-remove" @click="unlinkTransaction(lt.id)">×</button>
                </div>
              </div>
              <div v-else class="linked-empty">紐付けされた取引はありません</div>
              <div v-if="canSearchLinks" class="link-search-row">
                <input type="text" v-model="linkSearchQuery" placeholder="取引を検索して紐付け..."
                  class="link-search-input" @input="onLinkSearch" @focus="showLinkResults = true">
              </div>
              <div v-if="canSearchLinks && showLinkResults && linkSearchResults.length > 0" class="link-search-results">
                <div v-for="sr in linkSearchResults" :key="sr.id" class="link-search-item" @click="linkTransaction(sr)">
                  <span class="linked-date">{{ sr.date }}</span>
                  <span class="linked-account">{{ sr.fundItem || sr.account }}</span>
                  <span class="linked-item-name">{{ sr.item }}</span>
                  <span :class="sr.type === 'income' ? 'linked-amount-income' : 'linked-amount-expense'">
                    {{ sr.type === 'income' ? '+' : '-' }}¥{{ formatExactInteger(sr.amount, sr.amount_exact) }}
                  </span>
                </div>
              </div>
            </div>
          </div>
          </div>

          <!-- 画像は広い画面ではフォームの横、狭い画面ではフォームの後に置く。 -->
          <section class="transaction-image-panel" aria-labelledby="transaction-images-title">
            <div class="image-panel-heading">
              <h4 id="transaction-images-title">画像</h4>
              <span>{{ activeImageCount }} / {{ MAX_IMAGE_COUNT }} 枚</span>
            </div>
            <p v-if="isEditMode && imagesLoading" class="image-panel-message" role="status">保存済み画像を読み込んでいます…</p>
            <div v-else-if="imagesError" class="image-panel-message" role="alert">
              保存済み画像を読み込めませんでした。
              <button type="button" class="image-retry" @click="loadExistingImages">再読み込み</button>
            </div>
            <div v-else-if="existingImages.length" class="image-preview-group">
              <h5>保存済みの画像</h5>
              <div class="image-previews">
              <figure v-for="image in existingImages" :key="image.id" class="image-preview" :class="{ 'pending-removal': removedImageIds.includes(image.id) }">
                <img v-if="imageURL(image)" :src="imageURL(image)" :alt="image.filename || '添付画像'">
                <div v-else class="image-unavailable">この画像は表示できません</div>
                <figcaption>{{ image.filename || '添付画像' }}</figcaption>
                <button type="button" class="image-remove" @click="toggleExistingImage(image.id)">
                  {{ removedImageIds.includes(image.id) ? '元に戻す' : '削除する' }}
                </button>
              </figure>
              </div>
            </div>
            <div v-if="attachedImages.length" class="image-preview-group">
              <h5>今回追加する画像</h5>
              <div class="image-previews">
              <figure v-for="(img, index) in attachedImages" :key="index" class="image-preview">
                <img :src="img.preview" :alt="img.filename">
                <figcaption>{{ img.filename }}</figcaption>
                <button type="button" class="image-remove" @click="removeImage(index)">追加を取り消す</button>
              </figure>
              </div>
            </div>
            <p v-if="removedImageIds.length" class="image-removal-note">{{ removedImageIds.length }} 枚の削除は「更新」を押した時に確定します。</p>
            <div class="image-upload-area" :class="{ 'drag-over': isDragOver }"
              @dragover.prevent="onImageDragOver" @dragleave="isDragOver = false" @drop.prevent="onImageDrop">
              <button type="button" class="image-add-button" :disabled="imagesLoading || imagesError" @click="triggerFileSelect">画像を追加</button>
              <small>JPEG / PNG / GIF / WebP、1枚5 MiB・最大10枚</small>
              <input ref="fileInput" type="file" accept="image/jpeg,image/png,image/gif,image/webp" multiple
                aria-label="追加する画像を選択" @change="onFileSelect" hidden>
            </div>
          </section>
        </div>
        <div v-if="formError" class="form-error" role="alert">{{ formError }}</div>
        <div class="modal-buttons transaction-footer-actions">
          <div>
            <template v-if="isEditMode && !confirmingDelete">
              <button type="button" class="delete-btn" @click="confirmingDelete = true">削除</button>
            </template>
            <template v-if="confirmingDelete">
              <span class="delete-confirm-label">本当に削除しますか？</span>
              <button type="button" class="delete-confirm-yes" @click="$emit('delete'); confirmingDelete = false">はい</button>
              <button type="button" class="delete-confirm-no" @click="confirmingDelete = false">いいえ</button>
            </template>
          </div>
          <div class="transaction-save-actions">
            <button type="button" class="cancel-btn" @click="!busy && $emit('close')">キャンセル</button>
            <button type="submit" class="ok-btn">{{ isEditMode ? '更新' : 'OK' }}</button>
          </div>
        </div>
        </fieldset>
        <p v-if="busy" role="status">処理しています…</p>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { getTags, getTransactionLinks, getTransactions, getTransactionImages, isWailsMode } from '../utils/api'
import { formatExactInteger } from '../utils/exactAmount'

const MAX_TRANSACTION_AMOUNT = 1_000_000_000

const props = defineProps({
  isEditMode: Boolean,
  busy: { type: Boolean, default: false },
  transaction: Object,
  initialRemoveImageId: { type: Number, default: null },
  fundItems: { type: Array, default: () => [] },
  itemNames: { type: Array, default: () => [] },
  creditCardItems: { type: Array, default: () => [] },
  bankAccountItems: { type: Array, default: () => [] }
})

const emit = defineEmits(['save', 'delete', 'close'])

const showFundItemDropdown = ref(false)
const isDragOver = ref(false)
const confirmingDelete = ref(false)
const formError = ref('')
const attachedImages = ref([])
const existingImages = ref([])
const removedImageIds = ref([])
const imagesLoading = ref(false)
const imagesError = ref(false)
const fileInput = ref(null)
const allTags = ref([])
const selectedTags = ref([])
const selectedLevel1 = ref('')
const selectedLevel2 = ref('')
const selectedLevel3 = ref('')
const newTagName = ref('')
let nextPendingTagID = -1

const form = ref({
  date: new Date().toISOString().slice(0, 10),
  time: '',
  fundItem: '',
  type: 'expense',
  item: '',
  amount: '',
  memo: ''
})

const isNewFundItem = computed(() => {
  return form.value.fundItem && !props.fundItems.includes(form.value.fundItem)
})

const isNewItem = computed(() => {
  return form.value.item && !props.itemNames.includes(form.value.item)
})

const currentFundItem = computed(() => form.value.fundItem || props.transaction?.account || props.transaction?.fundItem || '')
const isCurrentCreditCard = computed(() => props.creditCardItems.includes(currentFundItem.value))
const isCurrentBankAccount = computed(() => props.bankAccountItems.includes(currentFundItem.value))
const canSearchLinks = computed(() => isCurrentCreditCard.value || isCurrentBankAccount.value)
const showLinkSection = computed(() => props.isEditMode && props.transaction?.id && (canSearchLinks.value || linkedTransactions.value.length > 0))
const linkHint = computed(() => {
  if (isCurrentCreditCard.value) return '銀行口座項目の引き落とし取引だけを紐付けできます'
  if (isCurrentBankAccount.value) return 'クレジットカード項目の支払い取引だけを紐付けできます'
  return '紐付けはクレジットカード項目と銀行口座項目の組み合わせだけで使えます'
})

// タグ階層
const level1Tags = computed(() => allTags.value)
const level2Tags = computed(() => {
  if (!selectedLevel1.value) return []
  const parent = allTags.value.find(t => t.id === Number(selectedLevel1.value))
  return parent?.children || []
})
const level3Tags = computed(() => {
  if (!selectedLevel2.value) return []
  const parent = level2Tags.value.find(t => t.id === Number(selectedLevel2.value))
  return parent?.children || []
})

function onLevel1Change() {
  selectedLevel2.value = ''
  selectedLevel3.value = ''
}
function onLevel2Change() {
  selectedLevel3.value = ''
}

function buildTagPath(tags, targetId) {
  const trail = []
  function search(nodes) {
    for (const node of nodes) {
      trail.push(node.name)
      if (node.id === targetId) return true
      if (node.children && search(node.children)) return true
      trail.pop()
    }
    return false
  }
  search(tags)
  return trail.join(' / ')
}

function getTagPath(tag) {
  if (tag.path) return tag.path
  const path = buildTagPath(allTags.value, tag.id)
  return path || tag.name
}

function addSelectedTag() {
  const tagId = Number(selectedLevel3.value || selectedLevel2.value || selectedLevel1.value)
  if (!tagId) return
  if (selectedTags.value.some(t => t.id === tagId)) return

  let tag = findTagById(allTags.value, tagId)
  if (tag) {
    const fullPath = buildTagPath(allTags.value, tag.id)
    selectedTags.value.push({ id: tag.id, name: tag.name, path: fullPath })
  }
}

function findTagById(tags, id) {
  for (const t of tags) {
    if (t.id === id) return t
    if (t.children) {
      const found = findTagById(t.children, id)
      if (found) return found
    }
  }
  return null
}

function removeTag(tagId) {
  selectedTags.value = selectedTags.value.filter(t => t.id !== tagId)
}

function createNewTag() {
  if (!newTagName.value.trim()) return
  const input = newTagName.value.trim()
  const parentID = Number(selectedLevel2.value || selectedLevel1.value) || null
  const parentPath = parentID ? buildTagPath(allTags.value, parentID) : ''
  const path = input.includes('/') || !parentPath ? input : `${parentPath}/${input}`
  const segments = path.split('/').map(segment => segment.trim())
  if (segments.length > 3 || segments.some(segment => !segment)) {
    formError.value = 'タグは空の名前を含めず3階層までで指定してください'
    return
  }
  const canonicalPath = segments.join('/')
  if (!selectedTags.value.some(tag => getTagPath(tag).split('/').map(segment => segment.trim()).join('/') === canonicalPath)) {
    selectedTags.value.push({ id: nextPendingTagID--, name: segments[segments.length - 1], path: canonicalPath, pending: true })
  }
  newTagName.value = ''
  formError.value = ''
}

async function loadTags() {
  try {
    const tags = await getTags()
    if (active) allTags.value = tags
  } catch (e) {
    if (active) formError.value = 'タグ一覧の取得に失敗しました: ' + e.message
  }
}

// --- 取引紐付け (Agent.md §6.2) ---
const linkedTransactions = ref([])
const pendingLinkAddIDs = ref([])
const pendingLinkRemoveIDs = ref([])
const linkSearchQuery = ref('')
const linkSearchResults = ref([])
const showLinkResults = ref(false)
let linkSearchTimer = null

async function loadLinkedTransactions() {
  if (!props.isEditMode || !props.transaction?.id) return
  try {
    const links = await getTransactionLinks(props.transaction.id)
    if (active) linkedTransactions.value = links
  } catch (e) {
    if (active) formError.value = '紐付け一覧の取得に失敗しました: ' + e.message
  }
}

function onLinkSearch() {
  clearTimeout(linkSearchTimer)
  if (!linkSearchQuery.value.trim()) {
    linkSearchResults.value = []
    showLinkResults.value = false
    return
  }
  linkSearchTimer = setTimeout(async () => {
    try {
      const all = await getTransactions('', linkSearchQuery.value.trim())
      if (!active) return
      const currentId = props.transaction?.id
      const linkedIds = new Set(linkedTransactions.value.map(lt => lt.id))
      linkSearchResults.value = (all || [])
        .filter(t => t.id !== currentId && !linkedIds.has(t.id))
        .filter(isLinkCounterpart)
        .slice(0, 10)
      showLinkResults.value = true
    } catch (e) {
      if (active) {
        formError.value = '紐付け候補の検索に失敗しました: ' + e.message
        showLinkResults.value = false
      }
    }
  }, 300)
}

function isLinkCounterpart(tx) {
  const account = tx.account || tx.fundItem || ''
  if (isCurrentCreditCard.value) {
    return props.bankAccountItems.includes(account)
  }
  if (isCurrentBankAccount.value) {
    return props.creditCardItems.includes(account)
  }
  return false
}

function linkTransaction(tx) {
  if (pendingLinkRemoveIDs.value.includes(tx.id)) {
    pendingLinkRemoveIDs.value = pendingLinkRemoveIDs.value.filter(id => id !== tx.id)
  } else if (!pendingLinkAddIDs.value.includes(tx.id)) {
    pendingLinkAddIDs.value = [...pendingLinkAddIDs.value, tx.id]
  }
  linkedTransactions.value = [...linkedTransactions.value, { ...tx, fundItem: tx.fundItem || tx.account }]
  linkSearchQuery.value = ''
  linkSearchResults.value = []
  showLinkResults.value = false
}

function unlinkTransaction(linkedId) {
  linkedTransactions.value = linkedTransactions.value.filter(link => link.id !== linkedId)
  if (pendingLinkAddIDs.value.includes(linkedId)) {
    pendingLinkAddIDs.value = pendingLinkAddIDs.value.filter(id => id !== linkedId)
  } else if (!pendingLinkRemoveIDs.value.includes(linkedId)) {
    pendingLinkRemoveIDs.value = [...pendingLinkRemoveIDs.value, linkedId]
  }
}

// 画像添付
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
// Web APIはBase64を含むリクエスト全体が10 MiB上限のため、原データを7 MiBに抑える。
const MAX_IMAGE_TOTAL_BYTES = (isWailsMode ? 20 : 7) * 1024 * 1024
const MAX_IMAGE_COUNT = 10
const ALLOWED_IMAGE_TYPES = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/webp'])
let pendingImageCount = 0
let pendingImageBytes = 0
let active = true
let imagesRequest = 0
const pendingReaders = new Set()
const activeImageCount = computed(() => existingImages.value.length - removedImageIds.value.length + attachedImages.value.length)

function imageURL(image) {
  if (image.invalid || typeof image.data_url !== 'string') return ''
  return /^data:image\/(?:jpeg|png|gif|webp);base64,[A-Za-z0-9+/=]+$/.test(image.data_url)
    ? image.data_url
    : ''
}

async function loadExistingImages() {
  if (!props.isEditMode || !props.transaction?.id) return
  const request = ++imagesRequest
  imagesLoading.value = true
  imagesError.value = false
  try {
    const result = await getTransactionImages(props.transaction.id)
    if (!active || request !== imagesRequest) return
    existingImages.value = result
    removedImageIds.value = result.some(image => image.id === props.initialRemoveImageId)
      ? [props.initialRemoveImageId]
      : []
  } catch {
    if (active && request === imagesRequest) imagesError.value = true
  } finally {
    if (active && request === imagesRequest) imagesLoading.value = false
  }
}

function toggleExistingImage(imageId) {
  if (removedImageIds.value.includes(imageId)) {
    removedImageIds.value = removedImageIds.value.filter(id => id !== imageId)
  } else {
    removedImageIds.value = [...removedImageIds.value, imageId]
  }
}

function triggerFileSelect() {
  if (!props.busy && !imagesLoading.value && !imagesError.value && fileInput.value) {
    fileInput.value.click()
  }
}

function onImageDragOver() {
  if (!props.busy && !imagesLoading.value && !imagesError.value) isDragOver.value = true
}

function onAmountInput(e) {
  form.value.amount = e.target.value.replace(/[^0-9]/g, '')
}

function onFileSelect(e) {
  const files = Array.from(e.target.files)
  processFiles(files)
  e.target.value = ''
}

function onImageDrop(e) {
  isDragOver.value = false
  if (props.busy || imagesLoading.value || imagesError.value) return
  const files = Array.from(e.dataTransfer.files)
  processFiles(files)
}

function processFiles(files) {
  if (props.busy || imagesLoading.value || imagesError.value) return
  let acceptedBytes = attachedImages.value.reduce((total, image) => total + (image.size || 0), 0) + pendingImageBytes
  let acceptedCount = activeImageCount.value + pendingImageCount

  for (const file of files) {
    if (acceptedCount >= MAX_IMAGE_COUNT) {
      formError.value = `画像は1取引につき${MAX_IMAGE_COUNT}件までです`
      break
    }
    if (!ALLOWED_IMAGE_TYPES.has(file.type)) {
      formError.value = `${file.name}: JPEG、PNG、GIF、WebPのみ使用できます`
      continue
    }
    if (file.size <= 0 || file.size > MAX_IMAGE_BYTES) {
      formError.value = `${file.name}: 画像は1件につき5 MiBまでです`
      continue
    }
    if (acceptedBytes + file.size > MAX_IMAGE_TOTAL_BYTES) {
      formError.value = `画像データの合計は1取引につき${isWailsMode ? 20 : 7} MiBまでです`
      break
    }

    acceptedCount++
    acceptedBytes += file.size
    pendingImageCount++
    pendingImageBytes += file.size
    const reader = new FileReader()
    pendingReaders.add(reader)
    reader.onload = (e) => {
      pendingReaders.delete(reader)
      if (!active) return
      const base64 = e.target.result.split(',')[1]
      attachedImages.value.push({
        filename: file.name,
        data: base64,
        mime_type: file.type,
        size: file.size,
        preview: e.target.result
      })
      pendingImageCount--
      pendingImageBytes -= file.size
    }
    reader.onerror = () => {
      pendingReaders.delete(reader)
      if (!active) return
      pendingImageCount--
      pendingImageBytes -= file.size
      formError.value = `${file.name}: 画像の読み込みに失敗しました`
    }
    reader.readAsDataURL(file)
  }
}

function removeImage(index) {
  attachedImages.value.splice(index, 1)
}

function handleSubmit() {
  if (props.busy) return
  if (props.isEditMode && (imagesLoading.value || imagesError.value)) {
    formError.value = '保存済み画像の読み込みが完了してから更新してください'
    return
  }
  if (pendingImageCount > 0) {
    formError.value = '画像の読み込みが完了するまでお待ちください'
    return
  }
  const amount = parseInt(form.value.amount)
  if (!amount || amount <= 0) {
    formError.value = '金額は正の数値である必要があります'
    return
  }
  if (!Number.isSafeInteger(amount) || amount > MAX_TRANSACTION_AMOUNT) {
    formError.value = `金額は${MAX_TRANSACTION_AMOUNT.toLocaleString('ja-JP')}円以下で指定してください`
    return
  }
  formError.value = ''

  const data = {
    account: form.value.fundItem,
    date: form.value.date,
    time: form.value.time,
    item: form.value.item,
    type: form.value.type,
    amount: amount,
    memo: form.value.memo,
    tags: selectedTags.value.filter(tag => !tag.pending).map(tag => tag.id)
  }

  const newTagPaths = selectedTags.value.filter(tag => tag.pending).map(tag => tag.path)
  if (newTagPaths.length) data.new_tag_paths = newTagPaths
  if (pendingLinkAddIDs.value.length) data.link_add_ids = [...pendingLinkAddIDs.value]
  if (pendingLinkRemoveIDs.value.length) data.link_remove_ids = [...pendingLinkRemoveIDs.value]

  // 画像がある場合はBase64で含める
  if (attachedImages.value.length > 0) {
    data.images = attachedImages.value.map(img => ({
      filename: img.filename,
      data: img.data,
      mime_type: img.mime_type
    }))
  }
  if (removedImageIds.value.length > 0) data.delete_image_ids = [...removedImageIds.value]

  emit('save', data)
}

onMounted(async () => {
  if (props.isEditMode && props.transaction?.id) loadExistingImages()
  await loadTags()
  if (!active) return

  if (props.isEditMode && props.transaction) {
    const tx = props.transaction
    const dateParts = (tx.date || '').split(' ')
    form.value.date = dateParts[0] || ''
    form.value.time = dateParts[1] ? dateParts[1].slice(0, 5) : ''
    form.value.fundItem = tx.account || tx.fundItem || ''
    form.value.type = tx.type || 'expense'
    form.value.item = tx.item || ''
    form.value.amount = String(tx.amount_exact ?? tx.amount ?? '')
    form.value.memo = tx.memo || ''

    // 既存タグをロード
    if (tx.tags && tx.tags.length > 0) {
      selectedTags.value = tx.tags.map(t => {
        const fullPath = buildTagPath(allTags.value, t.id)
        return { id: t.id, name: t.name, path: fullPath }
      })
    }

    // 紐付け取引をロード
    await loadLinkedTransactions()
  }
})

onBeforeUnmount(() => {
  active = false
  imagesRequest++
  clearTimeout(linkSearchTimer)
  for (const reader of pendingReaders) reader.abort()
  pendingReaders.clear()
  attachedImages.value = []
  existingImages.value = []
  removedImageIds.value = []
  selectedTags.value = []
  linkedTransactions.value = []
  linkSearchResults.value = []
  form.value = { date: '', time: '', fundItem: '', type: 'expense', item: '', amount: '', memo: '' }
})
</script>

<style scoped>
.transaction-fields {
  border: 0;
  margin: 0;
  padding: 0;
  min-width: 0;
  min-height: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.form-container {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(0, 0.85fr);
  align-content: start;
  gap: 1.5rem;
}
.transaction-primary-fields,
.transaction-image-panel {
  min-width: 0;
}
.transaction-image-panel {
  padding: 1rem;
  border: 1px solid #dce2f5;
  border-radius: .85rem;
  background: #f8f9ff;
}
.image-panel-heading {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: .75rem;
  margin-bottom: .75rem;
}
.image-panel-heading h4,
.image-preview-group h5 { margin: 0; }
.image-panel-heading span,
.image-preview-group h5 { color: #5e6664; font-size: .85rem; }
.image-preview-group { margin-bottom: 1rem; }
.image-preview-group h5 { margin-bottom: .5rem; }
.image-panel-message { margin: 0 0 .75rem; }
.image-retry { margin-left: .4rem; border: 0; background: transparent; color: #4358b4; cursor: pointer; text-decoration: underline; }
.image-removal-note { margin: 0 0 .75rem; color: #8a2525; font-size: .85rem; }
.transaction-footer-actions { justify-content: space-between; align-items: center; flex-wrap: wrap; }
.transaction-save-actions { display: flex; gap: .5rem; margin-left: auto; }
@media (max-width: 700px) {
  .form-container { grid-template-columns: minmax(0, 1fr); gap: 1rem; }
  .transaction-image-panel { padding: .8rem; }
  .transaction-footer-actions { gap: .6rem; }
}
@media (max-width: 480px) {
  .transaction-save-actions { width: 100%; justify-content: flex-end; }
}
/* タグセレクター */
.tag-selector {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  box-sizing: border-box;
}
.selected-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.tag-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  background: rgba(102, 126, 234, 0.12);
  border: 1px solid rgba(102, 126, 234, 0.3);
  border-radius: 12px;
  font-size: 0.8em;
  color: #555;
}
.tag-remove {
  background: none;
  border: none;
  color: #dc3545;
  cursor: pointer;
  font-size: 1em;
  padding: 0;
  line-height: 1;
}
.tag-dropdown-group {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.tag-select {
  flex: 1;
  min-width: 80px;
  padding: 6px 8px;
  border-radius: 8px;
  border: 1px solid #ddd;
  background: white;
  color: #333;
  font-size: 0.85em;
}
.tag-select:focus {
  border-color: #667eea;
  outline: none;
}
.new-tag-row {
  display: flex;
  gap: 4px;
}
.new-tag-input {
  flex: 1;
  padding: 6px 8px;
  border-radius: 8px;
  border: 1px solid #ddd;
  background: white;
  color: #333;
  font-size: 0.85em;
}
.new-tag-input:focus {
  border-color: #667eea;
  outline: none;
}
.add-tag-btn {
  padding: 6px 12px;
  border-radius: 8px;
  border: none;
  background: #667eea;
  color: white;
  cursor: pointer;
  font-size: 0.8em;
  transition: background 0.2s;
}
.add-tag-btn:hover {
  background: #5a6fd6;
}

/* 画像アップロード */
.image-upload-area {
  width: 100%;
  min-height: 100px;
  border: 2px dashed rgba(102, 126, 234, 0.4);
  border-radius: 12px;
  padding: 12px;
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  background: rgba(102, 126, 234, 0.03);
  box-sizing: border-box;
}
.image-upload-area:hover {
  border-color: rgba(102, 126, 234, 0.7);
  background: rgba(102, 126, 234, 0.06);
}
.image-upload-area.drag-over {
  border-color: rgba(106, 168, 79, 0.8);
  background: rgba(106, 168, 79, 0.08);
}
.image-upload-area small { color: #5e6664; font-size: .8rem; }
.image-add-button {
  border: 0;
  border-radius: .55rem;
  padding: .55rem .9rem;
  background: #667eea;
  color: white;
  font: inherit;
  cursor: pointer;
}
.image-add-button:hover { background: #5268cf; }
.image-add-button:disabled { opacity: .55; cursor: not-allowed; }
.image-previews {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(132px, 1fr));
  gap: .6rem;
}
.image-preview {
  display: flex;
  flex-direction: column;
  min-width: 0;
  margin: 0;
  padding: .5rem;
  border: 1px solid #dce2f5;
  border-radius: .65rem;
  background: #fff;
}
.image-preview img {
  width: 100%;
  height: 104px;
  object-fit: contain;
  border-radius: 6px;
  background: #f2f3f6;
}
.image-preview figcaption { margin: .4rem 0; font-size: .8rem; overflow-wrap: anywhere; }
.image-preview.pending-removal img { opacity: .4; }
.image-preview.pending-removal { border-color: #e6a2a2; background: #fff6f6; }
.image-unavailable { display: grid; place-items: center; height: 104px; font-size: .8rem; color: #8a2525; }
.image-remove {
  margin-top: auto;
  padding: .4rem .35rem;
  border: 1px solid #db8f8f;
  border-radius: .5rem;
  background: #fff;
  color: #982626;
  font: inherit;
  font-size: .8rem;
  cursor: pointer;
}
.image-remove:hover { background: #fff0f0; }
.delete-confirm-label {
  font-size: 0.8em;
  color: #d32f2f;
  margin-right: 6px;
}
.delete-confirm-yes {
  background: #d32f2f;
  border: none;
  color: #fff;
  padding: 4px 10px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
  margin-right: 4px;
}
.delete-confirm-yes:hover { background: #b71c1c; }
.delete-confirm-no {
  background: #f5f5f5;
  border: 1px solid #ddd;
  color: #666;
  padding: 4px 10px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
}
.delete-confirm-no:hover { background: #eee; }
.form-error {
  color: #d32f2f;
  font-size: 0.85em;
  padding: 6px 8px;
  background: #ffebee;
  border-radius: 6px;
  margin-bottom: 8px;
}

/* 取引紐付け (Agent.md §6.2) */
.link-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  box-sizing: border-box;
}
.link-hint {
  color: #666;
  font-size: 0.82em;
  line-height: 1.4;
}
.linked-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 150px;
  overflow-y: auto;
}
.linked-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: rgba(102, 126, 234, 0.06);
  border: 1px solid rgba(102, 126, 234, 0.15);
  border-radius: 8px;
  font-size: 0.82em;
}
.linked-info {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  overflow: hidden;
}
.linked-date {
  color: #888;
  white-space: nowrap;
  font-size: 0.9em;
}
.linked-account {
  color: #667eea;
  white-space: nowrap;
  font-size: 0.9em;
}
.linked-item-name {
  color: #333;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.linked-amount-income {
  color: #6aa84f;
  white-space: nowrap;
  font-weight: bold;
}
.linked-amount-expense {
  color: #d32f2f;
  white-space: nowrap;
  font-weight: bold;
}
.link-remove {
  background: none;
  border: none;
  color: #dc3545;
  cursor: pointer;
  font-size: 1.1em;
  padding: 0 4px;
  line-height: 1;
  flex-shrink: 0;
}
.linked-empty {
  color: #999;
  font-size: 0.82em;
  padding: 4px 0;
}
.link-search-row {
  display: flex;
  gap: 4px;
}
.link-search-input {
  flex: 1;
  padding: 6px 8px;
  border-radius: 8px;
  border: 1px solid #ddd;
  background: white;
  color: #333;
  font-size: 0.85em;
}
.link-search-input:focus {
  border-color: #667eea;
  outline: none;
}
.link-search-results {
  max-height: 150px;
  overflow-y: auto;
  border: 1px solid #ddd;
  border-radius: 8px;
  background: white;
}
.link-search-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  cursor: pointer;
  font-size: 0.82em;
  transition: background 0.15s;
}
.link-search-item:hover {
  background: rgba(102, 126, 234, 0.08);
}
.link-search-item:not(:last-child) {
  border-bottom: 1px solid #f0f0f0;
}
</style>
