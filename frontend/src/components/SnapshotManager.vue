<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content snapshot-modal" @click.stop>
      <div class="snapshot-header">
        <h3>スナップショット管理</h3>
        <button class="close-btn" @click="$emit('close')">&times;</button>
      </div>

      <div class="snapshot-info-text">
        操作ごとに自動保存されます（最大30件）
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
          <div class="snapshot-actions">
            <template v-if="confirmingSnapshot === snapshot">
              <span class="confirm-label">復元しますか？</span>
              <button class="confirm-yes-btn" @click="executeRestore(snapshot)" :disabled="isRestoring">
                {{ isRestoring ? '復元中...' : 'はい' }}
              </button>
              <button class="confirm-no-btn" @click="confirmingSnapshot = null" :disabled="isRestoring">
                いいえ
              </button>
            </template>
            <template v-else>
              <button class="restore-btn" @click="confirmingSnapshot = snapshot" :disabled="isRestoring">
                復元
              </button>
            </template>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import {
  listSnapshots as apiListSnapshots,
  restoreSnapshot as apiRestoreSnapshot
} from '../utils/api'

const emit = defineEmits(['close', 'restored'])

const snapshots = ref([])
const isRestoring = ref(false)
const confirmingSnapshot = ref(null)
const message = ref('')
const messageType = ref('info')

function formatSnapshotName(name) {
  return name.replace('.db', '').replace('omni_money_', '')
}

function extractDate(name) {
  const match = name.match(/(\d{4})(\d{2})(\d{2})_(\d{2})(\d{2})(\d{2})/)
  if (!match) return ''
  return `${match[1]}/${match[2]}/${match[3]} ${match[4]}:${match[5]}:${match[6]}`
}

async function fetchSnapshots() {
  try {
    snapshots.value = await apiListSnapshots()
    snapshots.value.sort().reverse()
  } catch (e) {
    console.error('スナップショット一覧取得エラー:', e)
  }
}

async function executeRestore(name) {
  isRestoring.value = true
  message.value = ''
  try {
    await apiRestoreSnapshot(name)
    localStorage.setItem('snapshot_restored', 'success')
    setTimeout(() => {
      window.location.reload()
    }, 300)
  } catch (e) {
    localStorage.setItem('snapshot_restored', 'error:' + (e.message || '不明なエラー'))
    message.value = '復元に失敗しました: ' + e.message
    messageType.value = 'error'
    isRestoring.value = false
    confirmingSnapshot.value = null
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
  color: #333;
}

.snapshot-info-text {
  font-size: 0.85em;
  color: #888;
  margin-bottom: 12px;
}

.close-btn {
  background: none;
  border: none;
  color: #999;
  font-size: 1.5em;
  cursor: pointer;
  padding: 0 4px;
  line-height: 1;
}

.close-btn:hover {
  color: #333;
}

.snapshot-message {
  padding: 8px 12px;
  border-radius: 8px;
  margin-bottom: 12px;
  font-size: 0.9em;
}

.snapshot-message.success {
  background: #e6ffed;
  color: #155724;
  border: 1px solid #c3e6cb;
}

.snapshot-message.error {
  background: #ffe6e6;
  color: #721c24;
  border: 1px solid #f5c6cb;
}

.snapshot-message.info {
  background: #e7f3ff;
  color: #004085;
  border: 1px solid #b8daff;
}

.snapshot-list {
  flex: 1;
  overflow-y: auto;
}

.snapshot-empty {
  text-align: center;
  color: #999;
  padding: 24px;
}

.snapshot-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid #eee;
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
  color: #333;
}

.snapshot-date {
  font-size: 0.75em;
  color: #999;
}

.snapshot-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.confirm-label {
  font-size: 0.75em;
  color: #e65100;
  white-space: nowrap;
}

.confirm-yes-btn {
  background: #e65100;
  border: none;
  color: #fff;
  padding: 4px 10px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
  transition: all 0.2s;
}

.confirm-yes-btn:hover:not(:disabled) {
  background: #bf360c;
}

.confirm-yes-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.confirm-no-btn {
  background: #f5f5f5;
  border: 1px solid #ddd;
  color: #666;
  padding: 4px 10px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
  transition: all 0.2s;
}

.confirm-no-btn:hover:not(:disabled) {
  background: #eee;
}

.restore-btn {
  background: #fff3e0;
  border: 1px solid #ffcc80;
  color: #e65100;
  padding: 5px 12px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.8em;
  transition: all 0.2s;
}

.restore-btn:hover:not(:disabled) {
  background: #ffe0b2;
}

.restore-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
