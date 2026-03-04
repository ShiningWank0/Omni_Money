<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content snapshot-modal" @click.stop>
      <div class="snapshot-header">
        <h3>スナップショット管理</h3>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>

      <div class="snapshot-actions">
        <button class="ok-btn" @click="createSnapshot" :disabled="isCreating">
          {{ isCreating ? '作成中...' : '新しいスナップショットを作成' }}
        </button>
      </div>

      <div v-if="message" class="snapshot-message" :class="messageType">
        {{ message }}
      </div>

      <div class="snapshot-list">
        <div v-if="snapshots.length === 0" class="snapshot-empty">
          スナップショットはありません
        </div>
        <div v-for="snapshot in snapshots" :key="snapshot" class="snapshot-item">
          <div class="snapshot-info">
            <span class="snapshot-name">{{ formatSnapshotName(snapshot) }}</span>
            <span class="snapshot-date">{{ extractDate(snapshot) }}</span>
          </div>
          <button class="restore-btn" @click="restoreSnapshot(snapshot)" :disabled="isRestoring">
            復元
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import {
  createSnapshot as apiCreateSnapshot,
  listSnapshots as apiListSnapshots,
  restoreSnapshot as apiRestoreSnapshot
} from '../utils/api'

defineEmits(['close'])

const snapshots = ref([])
const isCreating = ref(false)
const isRestoring = ref(false)
const message = ref('')
const messageType = ref('info')

function formatSnapshotName(name) {
  return name.replace('.db', '').replace('omni_money_', '')
}

function extractDate(name) {
  // omni_money_20260304_093000.db → 2026/03/04 09:30
  const match = name.match(/(\d{4})(\d{2})(\d{2})_(\d{2})(\d{2})(\d{2})/)
  if (!match) return ''
  return `${match[1]}/${match[2]}/${match[3]} ${match[4]}:${match[5]}`
}

async function fetchSnapshots() {
  try {
    snapshots.value = await apiListSnapshots()
    // 新しい順にソート
    snapshots.value.sort().reverse()
  } catch (e) {
    console.error('スナップショット一覧取得エラー:', e)
  }
}

async function createSnapshot() {
  isCreating.value = true
  message.value = ''
  try {
    await apiCreateSnapshot()
    message.value = 'スナップショットを作成しました'
    messageType.value = 'success'
    await fetchSnapshots()
  } catch (e) {
    message.value = 'スナップショット作成に失敗しました: ' + e.message
    messageType.value = 'error'
  } finally {
    isCreating.value = false
  }
}

async function restoreSnapshot(name) {
  if (!confirm(`スナップショット「${formatSnapshotName(name)}」に復元しますか？\n\n現在のデータは上書きされます。この操作は取り消せません。`)) {
    return
  }

  isRestoring.value = true
  message.value = ''
  try {
    await apiRestoreSnapshot(name)
    message.value = 'スナップショットから復元しました。ページを再読み込みします...'
    messageType.value = 'success'
    setTimeout(() => {
      window.location.reload()
    }, 1500)
  } catch (e) {
    message.value = '復元に失敗しました: ' + e.message
    messageType.value = 'error'
    isRestoring.value = false
  }
}

onMounted(fetchSnapshots)
</script>

<style scoped>
.snapshot-modal {
  max-width: 500px;
  width: 90vw;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}

.snapshot-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.snapshot-header h3 {
  margin: 0;
  font-size: 1.1em;
}

.close-btn {
  background: none;
  border: none;
  color: rgba(255, 255, 255, 0.7);
  font-size: 1.5em;
  cursor: pointer;
  padding: 0 4px;
  line-height: 1;
}

.close-btn:hover {
  color: #fff;
}

.snapshot-actions {
  margin-bottom: 12px;
}

.snapshot-message {
  padding: 8px 12px;
  border-radius: 6px;
  margin-bottom: 12px;
  font-size: 0.9em;
}

.snapshot-message.success {
  background: rgba(100, 220, 130, 0.2);
  color: rgba(100, 220, 130, 1);
  border: 1px solid rgba(100, 220, 130, 0.3);
}

.snapshot-message.error {
  background: rgba(255, 100, 100, 0.2);
  color: rgba(255, 100, 100, 1);
  border: 1px solid rgba(255, 100, 100, 0.3);
}

.snapshot-message.info {
  background: rgba(100, 180, 255, 0.2);
  color: rgba(100, 180, 255, 1);
  border: 1px solid rgba(100, 180, 255, 0.3);
}

.snapshot-list {
  flex: 1;
  overflow-y: auto;
}

.snapshot-empty {
  text-align: center;
  color: rgba(255, 255, 255, 0.5);
  padding: 24px;
}

.snapshot-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.snapshot-item:last-child {
  border-bottom: none;
}

.snapshot-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.snapshot-name {
  font-size: 0.85em;
  font-family: monospace;
  color: rgba(255, 255, 255, 0.8);
}

.snapshot-date {
  font-size: 0.75em;
  color: rgba(255, 255, 255, 0.5);
}

.restore-btn {
  background: rgba(255, 180, 60, 0.2);
  border: 1px solid rgba(255, 180, 60, 0.4);
  color: rgba(255, 180, 60, 1);
  padding: 5px 12px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
  transition: all 0.2s;
}

.restore-btn:hover:not(:disabled) {
  background: rgba(255, 180, 60, 0.35);
}

.restore-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
