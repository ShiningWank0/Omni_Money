<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content graph-modal-content graph-modal-xlarge csv-import-modal" @click.stop>
      <h3 style="margin: 8px 0 12px 0; font-size: 1.3em;">CSVインポート</h3>

      <!-- ファイル選択セクション -->
      <div class="graph-filter-row">
        <label>CSVファイル：</label>
        <div style="flex: 1; min-width: 200px;">
          <input type="file" accept=".csv" ref="csvFileInput" @change="onCSVFileSelected">
          <div v-if="csvFile" class="file-info">
            選択ファイル: {{ csvFile.name }}
          </div>
        </div>
      </div>

      <!-- インポートモード選択 -->
      <div class="graph-filter-row">
        <label>インポートモード：</label>
        <div style="display: flex; gap: 16px; flex-wrap: wrap;">
          <label style="display: flex; align-items: center; cursor: pointer; font-size: 0.9em;">
            <input type="radio" v-model="csvImportMode" value="append">
            <span>追加 (既存データを保持)</span>
          </label>
          <label style="display: flex; align-items: center; cursor: pointer; font-size: 0.9em;">
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
      <div v-if="csvImporting" style="margin-bottom: 12px;">
        <div class="progress-bar">
          <div style="width: 100%; height: 100%; background: linear-gradient(90deg, #007bff, #0056b3); animation: progress-animation 1.5s infinite;"></div>
        </div>
        <div style="text-align: center; margin-top: 8px; font-size: 0.9em; color: #666; font-weight: 500;">
          CSVファイルをインポート中...
        </div>
      </div>

      <!-- ステータスメッセージ -->
      <div class="status-area">
        <div v-if="csvImportError" class="status-message status-error">{{ csvImportError }}</div>
        <div v-if="csvImportSuccess" class="status-message status-success">{{ csvImportSuccess }}</div>
      </div>

      <!-- ボタン -->
      <div style="display: flex; justify-content: space-between; align-items: center; margin-top: 8px;">
        <button class="cancel-btn" @click="$emit('close')" :disabled="csvImporting" style="padding: 6px 16px;">キャンセル</button>
        <button class="ok-btn" @click="importCSVFile" :disabled="!csvFile || csvImporting"
          :style="{ opacity: (!csvFile || csvImporting) ? 0.5 : 1 }"
          style="padding: 6px 20px;">
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
