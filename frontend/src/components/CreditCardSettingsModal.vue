<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content cc-settings-modal" @click.stop>
      <h3>クレジットカード設定</h3>

      <div class="info-box">
        <p>
          <strong>設定の効果：</strong><br>
          • クレジットカードとして設定した項目は残高計算から除外されます<br>
          • 取引履歴では残高表示が「-」になります<br>
          • 2重会計を防ぎ、正確な残高管理が可能です
        </p>
      </div>

      <div class="form-row">
        <label>クレジットカード項目：</label>
        <div class="select-wrapper">
          <button @click="showDropdown = !showDropdown" class="select-button">
            {{ displayText }} ▼
          </button>
          <div v-if="showDropdown" class="select-dropdown">
            <div class="dropdown-hint">利用可能な資金項目から選択してください</div>
            <label v-for="item in fundItems" :key="item" class="dropdown-item"
              :class="{ selected: localSelected.includes(item) }">
              <input type="checkbox" :checked="localSelected.includes(item)" @change="toggleItem(item)">
              <span>{{ item }}</span>
            </label>
          </div>
        </div>
      </div>

      <div class="action-buttons">
        <button @click="handleSave" class="ok-btn">設定を保存</button>
        <button @click="handleReset" class="delete-btn">設定をクリア</button>
      </div>

      <div v-if="message" class="status-message"
        :class="message.includes('成功') ? 'status-success' : 'status-error'">
        {{ message }}
      </div>

      <div class="close-section">
        <button class="cancel-btn" @click="$emit('close')">閉じる</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'

const props = defineProps({
  fundItems: { type: Array, default: () => [] },
  selectedItems: { type: Array, default: () => [] }
})

const emit = defineEmits(['save', 'close'])

const showDropdown = ref(false)
const localSelected = ref([])
const message = ref('')

const displayText = computed(() => {
  if (localSelected.value.length === 0) return '選択なし'
  if (localSelected.value.length === 1) return localSelected.value[0]
  return `${localSelected.value.length}件選択中`
})

function toggleItem(item) {
  const idx = localSelected.value.indexOf(item)
  if (idx >= 0) {
    localSelected.value.splice(idx, 1)
  } else {
    localSelected.value.push(item)
  }
}

function handleSave() {
  emit('save', [...localSelected.value])
  message.value = '設定を保存しました（成功）'
}

function handleReset() {
  localSelected.value = []
  emit('save', [])
  message.value = '設定をクリアしました（成功）'
}

onMounted(() => {
  localSelected.value = [...props.selectedItems]
})
</script>

<style scoped>
.cc-settings-modal {
  max-width: 500px;
}

.cc-settings-modal h3 {
  margin-top: 0;
  margin-bottom: 1rem;
  color: #333;
  text-align: center;
}

.info-box {
  margin-bottom: 1rem;
  padding: 12px;
  background: #e7f3ff;
  border-radius: 8px;
  border-left: 4px solid #667eea;
}

.info-box p {
  margin: 0;
  font-size: 0.9em;
  color: #333;
  line-height: 1.6;
}

.form-row {
  margin-bottom: 1rem;
}

.form-row label {
  display: block;
  margin-bottom: 0.5rem;
  font-weight: 500;
  color: #333;
}

.form-row label.dropdown-item {
  display: flex;
  margin-bottom: 0;
  font-weight: normal;
}

.select-wrapper {
  position: relative;
}

.select-button {
  width: 100%;
  text-align: left;
  padding: 0.75rem;
  border: 1px solid #ddd;
  border-radius: 8px;
  background: white;
  cursor: pointer;
  font-size: 0.95em;
  color: #333;
  transition: border-color 0.2s;
}

.select-button:hover {
  border-color: #667eea;
}

.select-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  background: white;
  border: 1px solid #ddd;
  border-radius: 8px;
  max-height: 250px;
  overflow-y: auto;
  z-index: 2000;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
  margin-top: 4px;
  padding: 8px;
}

.dropdown-hint {
  font-size: 0.85em;
  color: #999;
  border-bottom: 1px solid #eee;
  padding-bottom: 6px;
  margin-bottom: 6px;
}

.dropdown-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px;
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.2s;
}

.dropdown-item:hover {
  background: #f8f9fa;
}

.dropdown-item.selected {
  background: rgba(102, 126, 234, 0.08);
}

.dropdown-item input {
  margin: 0;
}

.action-buttons {
  display: flex;
  justify-content: center;
  gap: 12px;
  margin: 1rem 0;
}

.status-message {
  padding: 10px 12px;
  border-radius: 8px;
  text-align: center;
  font-weight: 500;
  font-size: 0.9em;
  margin-bottom: 1rem;
}

.status-success {
  background: #e6ffed;
  color: #155724;
  border: 1px solid #c3e6cb;
}

.status-error {
  background: #ffe6e6;
  color: #721c24;
  border: 1px solid #f5c6cb;
}

.close-section {
  text-align: center;
}
</style>
