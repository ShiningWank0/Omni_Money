<template>
  <div class="modal-overlay" @click="$emit('close')">
    <div class="modal-content csv-import-modal" @click.stop>
      <h3>CSVインポート</h3>

      <!-- ファイル選択セクション -->
      <div class="form-row">
        <label>CSVファイル：</label>
        <div>
          <input v-if="!isWailsMode" type="file" accept=".csv" ref="csvFileInput" :disabled="csvImporting" @change="onCSVFileSelected">
          <div v-if="isWailsMode" class="file-info">
            Desktopでは実行時にOSのファイル選択ダイアログを開きます。
          </div>
          <div v-else-if="csvFile" class="file-info">
            選択ファイル: {{ csvFile.name }}
          </div>
        </div>
      </div>

      <!-- インポートモード選択 -->
      <div class="form-row">
        <label>インポートモード：</label>
        <div class="radio-group">
          <label class="radio-label">
            <input type="radio" v-model="csvImportMode" value="append" :disabled="csvImporting" @change="onImportModeChanged">
            <span>追加 (既存データを保持)</span>
          </label>
          <label class="radio-label">
            <input type="radio" v-model="csvImportMode" value="replace" :disabled="csvImporting" @change="onImportModeChanged">
            <span>置換 (既存データを削除)</span>
          </label>
        </div>
      </div>

      <div v-if="csvImportMode === 'replace'" class="replace-warning" role="alert">
        <strong>破壊的操作です。</strong>
        置換を実行すると、現在の取引・画像・タグ・取引タグ・取引リンクと、allowlist対象のledger設定
        （credit_card_items / bank_account_items）を必ず削除します。CSVに設定行があればその値で置き換え、
        なければ未設定のままになります。AI連携の重複排除・日次利用量記録もリセットされます。
        その他の設定は保持され、CSVにない取引関連データは復元されません。
        <label class="replace-confirmation">
          <input v-model="replaceConfirmed" type="checkbox" :disabled="csvImporting">
          <span>現在の取引データが削除されることを理解し、置換を実行します</span>
        </label>
      </div>

      <!-- CSV形式の説明 -->
      <div class="format-info">
        <div style="font-weight: bold; margin-bottom: 8px; font-size: 0.95em; color: #333;">CSVファイル形式</div>
        <div>
          <div style="margin-bottom: 4px;"><strong>旧形式:</strong> account, date, item, type, amount（v1/v2も読み込み可能）</div>
          <div style="margin-bottom: 4px;"><strong>完全バックアップ:</strong> v3（取引・画像・タグ・タグ紐付け・取引リンク・ledger設定）</div>
          <div style="margin-left: 12px;">
            <div>• <strong>account:</strong> 資金項目名</div>
            <div>• <strong>date:</strong> 取引日 (YYYY-MM-DD または YYYY-MM-DD HH:MM:SS)</div>
            <div>• <strong>item:</strong> 取引項目名</div>
            <div>• <strong>type:</strong> income (収入) または expense (支出)</div>
            <div>• <strong>amount:</strong> 金額 (正の数値)</div>
            <div>• <strong>balance:</strong> 残高 (オプション、自動計算されます)</div>
            <div>• v3の画像はファイル名・MIMEタイプ・Base64バイナリを含みます</div>
            <div>• v3の関連付けはインポート時に安全な新しいIDへ再採番されます</div>
            <div>• appendは既存の取引・画像・タグ・リンク・ledger設定を保持します。CSVのledger設定が既存値と異なる場合は全体を中止します</div>
          </div>
          <div class="csv-plaintext-note" role="note">CSVは暗号化されない平文です。出力・保存前に、FileVault・BitLocker・LUKS等で保護された保存先であることを確認してください。</div>
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
        <button class="ok-btn" @click="importCSVFile" :disabled="importDisabled"
          :style="{ opacity: importDisabled ? 0.5 : 1 }">
          {{ csvImporting ? 'インポート中...' : 'インポート実行' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import { importCSV, isWailsMode } from '../utils/api'
import { canStartCSVImport } from '../utils/csvSafety'

const emit = defineEmits(['imported', 'close'])

const csvFile = ref(null)
const csvImportMode = ref('append')
const csvImporting = ref(false)
const csvImportError = ref('')
const csvImportSuccess = ref('')
const replaceConfirmed = ref(false)
const importDisabled = computed(() => !canStartCSVImport({
  hasFile: isWailsMode || Boolean(csvFile.value),
  importing: csvImporting.value,
  mode: csvImportMode.value,
  replaceConfirmed: replaceConfirmed.value
}))

function onCSVFileSelected(e) {
  csvFile.value = e.target.files[0] || null
  csvImportError.value = ''
  csvImportSuccess.value = ''
  replaceConfirmed.value = false
}

function onImportModeChanged() {
  replaceConfirmed.value = false
  csvImportError.value = ''
  csvImportSuccess.value = ''
}

async function importCSVFile() {
  if (!isWailsMode && !csvFile.value) return
  if (csvImportMode.value === 'replace' && !replaceConfirmed.value) {
    csvImportError.value = '置換によって現在の取引データが削除されることを確認してください'
    return
  }

  csvImporting.value = true
  csvImportError.value = ''
  csvImportSuccess.value = ''

  try {
    const count = await importCSV(isWailsMode ? null : csvFile.value, csvImportMode.value)
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

.replace-warning {
  margin-bottom: 12px;
  padding: 12px;
  color: #721c24;
  background: #fff3f3;
  border: 1px solid #e0a0a6;
  border-radius: 8px;
  font-size: 0.9em;
  line-height: 1.5;
}

.replace-confirmation {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 10px;
  color: #721c24;
  font-weight: 600;
  cursor: pointer;
}

.replace-confirmation input {
  flex: 0 0 auto;
  margin-top: 4px;
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

.csv-plaintext-note {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid #e0e0e0;
  color: #721c24;
  font-weight: 600;
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
