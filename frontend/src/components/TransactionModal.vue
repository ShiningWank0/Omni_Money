<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content transaction-modal" @click.stop>
      <h3>{{ isEditMode ? '取引を編集' : '新しい取引を追加' }}</h3>
      <form @submit.prevent="handleSubmit">
        <div class="form-container">
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
              autocomplete="off">
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
                  {{ getTagPath(tag) }}
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
                <input type="text" v-model="newTagName" placeholder="新規タグ名" class="new-tag-input">
                <button type="button" class="add-tag-btn" @click="createNewTag">作成</button>
              </div>
            </div>
          </div>

          <!-- 画像添付 (Agent.md §6.5) -->
          <div class="form-row">
            <label>画像:</label>
            <div class="image-upload-area"
              @dragover.prevent="isDragOver = true"
              @dragleave="isDragOver = false"
              @drop.prevent="onImageDrop"
              @click="triggerFileSelect"
              :class="{ 'drag-over': isDragOver }">
              <div class="image-previews" v-if="attachedImages.length > 0" @click.stop>
                <div v-for="(img, index) in attachedImages" :key="index" class="image-preview">
                  <img :src="img.preview" :alt="img.filename">
                  <button type="button" class="image-remove" @click="removeImage(index)">×</button>
                </div>
              </div>
              <div class="image-upload-placeholder">
                <span class="upload-icon">📷</span>
                <span>クリックまたはドラッグ&ドロップで画像を添付</span>
              </div>
              <input ref="fileInput" type="file" accept="image/jpeg,image/png,image/gif,image/webp" multiple
                @change="onFileSelect" style="display: none;">
            </div>
          </div>
        </div>
        <div v-if="formError" class="form-error">{{ formError }}</div>
        <div class="modal-buttons" style="display: flex; justify-content: space-between; align-items: center;">
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
          <div style="display: flex; gap: 8px;">
            <button type="button" class="cancel-btn" @click="$emit('close')">キャンセル</button>
            <button type="submit" class="ok-btn">{{ isEditMode ? '更新' : 'OK' }}</button>
          </div>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { getTags, createTag } from '../utils/api'

const props = defineProps({
  isEditMode: Boolean,
  transaction: Object,
  fundItems: { type: Array, default: () => [] },
  itemNames: { type: Array, default: () => [] }
})

const emit = defineEmits(['save', 'delete', 'close'])

const showFundItemDropdown = ref(false)
const isDragOver = ref(false)
const confirmingDelete = ref(false)
const formError = ref('')
const attachedImages = ref([])
const fileInput = ref(null)
const allTags = ref([])
const selectedTags = ref([])
const selectedLevel1 = ref('')
const selectedLevel2 = ref('')
const selectedLevel3 = ref('')
const newTagName = ref('')

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

function getTagPath(tag) {
  // タグ名をそのまま表示（フラットにしているため）
  return tag.name
}

function addSelectedTag() {
  const tagId = Number(selectedLevel3.value || selectedLevel2.value || selectedLevel1.value)
  if (!tagId) return
  if (selectedTags.value.some(t => t.id === tagId)) return

  // タグ情報を探す
  let tag = findTagById(allTags.value, tagId)
  if (tag) {
    selectedTags.value.push({ id: tag.id, name: tag.name })
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

async function createNewTag() {
  if (!newTagName.value.trim()) return
  const parentId = Number(selectedLevel2.value || selectedLevel1.value) || null
  try {
    const tag = await createTag(newTagName.value.trim(), parentId)
    newTagName.value = ''
    await loadTags()
    selectedTags.value.push({ id: tag.id, name: tag.name })
  } catch (e) {
    formError.value = 'タグ作成エラー: ' + e.message
  }
}

async function loadTags() {
  try {
    allTags.value = await getTags()
  } catch (e) {
    allTags.value = []
  }
}

// 画像添付
function triggerFileSelect() {
  if (fileInput.value) {
    fileInput.value.click()
  }
}

function onAmountInput(e) {
  form.value.amount = e.target.value.replace(/[^0-9]/g, '')
}

function onFileSelect(e) {
  const files = Array.from(e.target.files)
  processFiles(files)
}

function onImageDrop(e) {
  isDragOver.value = false
  const files = Array.from(e.dataTransfer.files).filter(f => f.type.startsWith('image/'))
  processFiles(files)
}

function processFiles(files) {
  for (const file of files) {
    const reader = new FileReader()
    reader.onload = (e) => {
      const base64 = e.target.result.split(',')[1]
      attachedImages.value.push({
        filename: file.name,
        data: base64,
        mime_type: file.type,
        preview: e.target.result
      })
    }
    reader.readAsDataURL(file)
  }
}

function removeImage(index) {
  attachedImages.value.splice(index, 1)
}

function handleSubmit() {
  const amount = parseInt(form.value.amount)
  if (!amount || amount <= 0) {
    formError.value = '金額は正の数値である必要があります'
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
    tags: selectedTags.value.map(t => t.id)
  }

  // 画像がある場合はBase64で含める
  if (attachedImages.value.length > 0) {
    data.images = attachedImages.value.map(img => ({
      filename: img.filename,
      data: img.data,
      mime_type: img.mime_type
    }))
  }

  emit('save', data)
}

onMounted(async () => {
  await loadTags()

  if (props.isEditMode && props.transaction) {
    const tx = props.transaction
    const dateParts = (tx.date || '').split(' ')
    form.value.date = dateParts[0] || ''
    form.value.time = dateParts[1] ? dateParts[1].slice(0, 5) : ''
    form.value.fundItem = tx.account || tx.fundItem || ''
    form.value.type = tx.type || 'expense'
    form.value.item = tx.item || ''
    form.value.amount = String(tx.amount || '')
    form.value.memo = tx.memo || ''

    // 既存タグをロード
    if (tx.tags && tx.tags.length > 0) {
      selectedTags.value = tx.tags.map(t => ({ id: t.id, name: t.name }))
    }
  }
})
</script>

<style scoped>
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
  min-height: 120px;
  border: 2px dashed rgba(102, 126, 234, 0.4);
  border-radius: 12px;
  padding: 16px;
  text-align: center;
  transition: all 0.2s;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
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
.image-previews {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}
.image-preview {
  position: relative;
  width: 60px;
  height: 60px;
}
.image-preview img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  border-radius: 6px;
  border: 1px solid #ddd;
}
.image-remove {
  position: absolute;
  top: -6px;
  right: -6px;
  background: #dc3545;
  border: 2px solid white;
  color: white;
  border-radius: 50%;
  width: 20px;
  height: 20px;
  font-size: 11px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}
.image-upload-placeholder {
  color: #999;
  font-size: 0.85em;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
}
.upload-icon {
  font-size: 2em;
}
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
</style>
