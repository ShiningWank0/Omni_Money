<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content csv-import-modal" @click.stop>
      <h3>CSVインポート</h3>

      <!-- ファイル選択セクション -->
      <div class="form-row">
        <label>CSVファイル：</label>
        <div>
          <input type="file" accept=".csv" ref="csvFileInput" @change="onCSVFileSelected">
          <div v-if="csvFile" class="file-info">
            選択ファイル: {{ csvFile.name }}
          </div>
        </div>
      </div>

      <!-- インポートモード選択 -->
      <div class="form-row">
        <label>インポートモード：</label>
        <div class="radio-group">
          <label class="radio-label">
            <input type="radio" v-model="csvImportMode" value="append">
            <span>追加 (既存データを保持)</span>
          </label>
          <label class="radio-label">
            <input type="radio" v-model="csvImportMode" value="replace">
            <span>置換 (既存データを削除)</span>
          </label>
        </div>
      </div>

      <!-- CSV形式の説明 -->
      <div class="format-info">
        <div style="font-weight: bold; margin-bottom: 8px; font-size: 0.95em; color: #333;">CSVファイル形式</div>
        <div>
          <div style="margin-bottom: 4px;"><strong>必須ヘッダー:</strong> account, date, item, type, amount</div>
          <div style="margin-left: 12px;">
            <div>• <strong>account:</strong> 資金項目名</div>
            <div>• <strong>date:</strong> 取引日 (YYYY-MM-DD または YYYY-MM-DD HH:MM:SS)</div>
            <div>• <strong>item:</strong> 取引項目名</div>
            <div>• <strong>type:</strong> income (収入) または expense (支出)</div>
            <div>• <strong>amount:</strong> 金額 (正の数値)</div>
            <div>• <strong>balance:</strong> 残高 (オプション、自動計算されます)</div>
          </div>
        </div>
      </div>

      <!-- プログレスバー -->
      <div v-if="csvImporting" class="progress-section">
        <div class="progress-bar">
          <div class="progress-fill"></div>
        </div>
        <div class="progress-text">CSVファイルをインポート中...</div>
      </div>

      <!-- ステータスメッセージ -->
      <div v-if="csvImportError" class="status-message status-error">{{ csvImportError }}</div>
      <div v-if="csvImportSuccess" class="status-message status-success">{{ csvImportSuccess }}</div>

      <!-- ボタン -->
      <div class="modal-buttons">
        <button class="cancel-btn" @click="$emit('close')" :disabled="csvImporting">キャンセル</button>
        <button class="ok-btn" @click="importCSVFile" :disabled="!csvFile || csvImporting"
          :style="{ opacity: (!csvFile || csvImporting) ? 0.5 : 1 }">
          {{ csvImporting ? 'インポート中...' : 'インポート実行' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { importCSV } from '../utils/api'

const emit = defineEmits(['imported', 'close'])

const csvFile = ref(null)
const csvImportMode = ref('append')
const csvImporting = ref(false)
const csvImportError = ref('')
const csvImportSuccess = ref('')

function onCSVFileSelected(e) {
  csvFile.value = e.target.files[0] || null
  csvImportError.value = ''
  csvImportSuccess.value = ''
}

async function importCSVFile() {
  if (!csvFile.value) return

  csvImporting.value = true
  csvImportError.value = ''
  csvImportSuccess.value = ''

  try {
    const content = await csvFile.value.text()
    const count = await importCSV(content, csvImportMode.value)
    csvImportSuccess.value = `CSVインポート完了: ${count}件のトランザクションを${csvImportMode.value === 'replace' ? '置換' : '追加'}しました`
    setTimeout(() => {
      emit('imported')
    }, 1500)
  } catch (e) {
    csvImportError.value = e.message || 'CSVインポートに失敗しました'
  } finally {
    csvImporting.value = false
  }
}
</script>

<style scoped>
.csv-import-modal {
  max-width: 560px;
}

.csv-import-modal h3 {
  margin-top: 0;
  margin-bottom: 1rem;
  color: #333;
  text-align: center;
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

.radio-group {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.radio-label {
  display: flex;
  align-items: center;
  cursor: pointer;
  font-size: 0.9em;
  color: #333;
}

.radio-label input {
  margin-right: 6px;
}

.file-info {
  margin-top: 4px;
  font-size: 0.85em;
  color: #666;
}

.format-info {
  margin-bottom: 12px;
  padding: 12px;
  background: #f8f9fa;
  border-radius: 8px;
  border: 1px solid #e0e0e0;
  font-size: 0.85em;
  color: #555;
  line-height: 1.5;
}

.progress-section {
  margin-bottom: 12px;
}

.progress-bar {
  height: 4px;
  background: #e0e0e0;
  border-radius: 2px;
  overflow: hidden;
}

.progress-fill {
  width: 100%;
  height: 100%;
  background: linear-gradient(90deg, #667eea, #764ba2);
  animation: progress-animation 1.5s infinite;
}

@keyframes progress-animation {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(100%); }
}

.progress-text {
  text-align: center;
  margin-top: 8px;
  font-size: 0.9em;
  color: #666;
  font-weight: 500;
}

.status-message {
  padding: 8px 12px;
  border-radius: 8px;
  margin-bottom: 12px;
  font-size: 0.9em;
}

.status-error {
  background: #ffe6e6;
  color: #721c24;
  border: 1px solid #f5c6cb;
}

.status-success {
  background: #e6ffed;
  color: #155724;
  border: 1px solid #c3e6cb;
}

.modal-buttons {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}
</style>
