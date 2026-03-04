<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content graph-modal-content graph-modal-xlarge" @click.stop>
      <h3 style="margin: 8px 0 12px 0; font-size: 1.3em;">クレジットカード設定</h3>

      <div style="margin-bottom: 20px; padding: 12px; background: #e3f2fd; border-radius: 4px; border-left: 4px solid #2196F3;">
        <p style="margin: 0; font-size: 0.9em; color: #1565C0;">
          <strong>設定の効果：</strong><br>
          • クレジットカードとして設定した項目は残高計算から除外されます<br>
          • 取引履歴では残高表示が「-」になります<br>
          • 2重会計を防ぎ、正確な残高管理が可能です
        </p>
      </div>

      <div class="graph-filter-row" style="margin-bottom: 20px; display: flex; align-items: center; gap: 8px;">
        <label style="white-space: nowrap; font-size: 0.95em; font-weight: bold;">クレジットカード項目：</label>
        <div class="multi-select-wrapper" style="position: relative;">
          <button @click="showDropdown = !showDropdown" class="multi-select-button"
            style="padding: 8px 12px; border: 1px solid #ccc; background: white; cursor: pointer; min-width: 200px; text-align: left;">
            {{ displayText }} ▼
          </button>
          <div v-if="showDropdown" class="multi-select-dropdown"
            style="position: absolute; top: 100%; left: 0; right: 0; background: white; border: 1px solid #ccc; max-height: 300px; overflow-y: auto; z-index: 2000;">
            <div style="padding: 8px;">
              <div style="margin-bottom: 8px; font-size: 0.9em; color: #666; border-bottom: 1px solid #eee; padding-bottom: 4px;">
                利用可能な資金項目から選択してください
              </div>
              <label v-for="item in fundItems" :key="item"
                style="display: block; padding: 4px 8px; cursor: pointer; border-radius: 2px;"
                :style="{ backgroundColor: localSelected.includes(item) ? '#f0f8ff' : 'transparent' }">
                <input type="checkbox" :checked="localSelected.includes(item)" @change="toggleItem(item)" style="margin-right: 8px;">
                <span style="font-size: 0.95em;">{{ item }}</span>
              </label>
            </div>
          </div>
        </div>
      </div>

      <div style="display: flex; justify-content: center; gap: 12px; margin: 20px 0;">
        <button @click="handleSave" class="menu-btn" style="padding: 8px 20px; background: #4CAF50; color: white; font-weight: bold;">設定を保存</button>
        <button @click="handleReset" class="menu-btn" style="padding: 8px 20px; background: #f44336; color: white;">設定をクリア</button>
      </div>

      <div v-if="message" style="margin: 16px 0; padding: 12px; border-radius: 4px; text-align: center; font-weight: bold;"
        :style="{
          backgroundColor: message.includes('成功') ? '#d4edda' : '#f8d7da',
          color: message.includes('成功') ? '#155724' : '#721c24',
          border: message.includes('成功') ? '1px solid #c3e6cb' : '1px solid #f5c6cb'
        }">
        {{ message }}
      </div>

      <button class="cancel-btn" @click="$emit('close')" style="margin-top: 16px; padding: 6px 16px;">閉じる</button>
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
