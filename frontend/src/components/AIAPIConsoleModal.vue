<template>
  <div class="modal-overlay" @click.self="$emit('close')">
    <div class="modal-content ai-console-modal" @click.stop>
      <div class="ai-console-header">
        <h3>AI API操作</h3>
        <button class="close-btn" type="button" @click="$emit('close')">&times;</button>
      </div>

      <div class="security-note">
        <strong>管理者向けAPI入力画面</strong>
        <p>送信内容は、通常Webのセッション認証を通過した後、サーバー内部からAI専用リスナーへ転送されます。AI用Bearer tokenはブラウザへ渡されません。</p>
        <p>ローカルLLMやクラウドLLMは、この画面へAPIキーを入力せず、別のローカル仲介プロセスからAI専用ポートを呼び出してください。</p>
      </div>

      <div class="form-row">
        <label for="ai-operation">操作:</label>
        <select id="ai-operation" v-model="operation" :disabled="sending">
          <option value="transactions">取引追加（POST /transactions）</option>
          <option value="analysis">分析（POST /analysis）</option>
        </select>
      </div>

      <div class="form-row">
        <label for="ai-request-body">JSONリクエスト:</label>
        <textarea
          id="ai-request-body"
          v-model="requestBody"
          rows="16"
          spellcheck="false"
          autocomplete="off"
          :disabled="sending"
        ></textarea>
      </div>

      <div class="button-row">
        <button type="button" class="cancel-btn" :disabled="sending" @click="$emit('close')">閉じる</button>
        <button type="button" class="send-btn" :disabled="sending" @click="sendRequest">
          {{ sending ? '送信中...' : 'AI専用入口へ送信' }}
        </button>
      </div>

      <div v-if="result" class="result-panel" :class="result.ok ? 'success' : 'error'">
        <div class="result-title">HTTP {{ result.status }}</div>
        <pre>{{ result.body }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'

const emit = defineEmits(['close', 'transaction-added'])

const operation = ref('transactions')
const requestBody = ref(defaultBody('transactions'))
const sending = ref(false)
const result = ref(null)

watch(operation, (value) => {
  requestBody.value = defaultBody(value)
  result.value = null
})

function defaultBody(value) {
  if (value === 'analysis') {
    return JSON.stringify({
      start_date: '',
      end_date: '',
      account: '',
      tag_ids: [],
      type: ''
    }, null, 2)
  }

  const today = new Date()
  const localDate = new Date(today.getTime() - today.getTimezoneOffset() * 60000).toISOString().slice(0, 10)
  return JSON.stringify({
    account: '現金',
    date: localDate,
    time: '',
    item: '食費',
    type: 'expense',
    amount: 0,
    memo: '',
    tags: [],
    images: []
  }, null, 2)
}

async function sendRequest() {
  result.value = null

  let payload
  try {
    payload = JSON.parse(requestBody.value)
  } catch (error) {
    result.value = { ok: false, status: '入力エラー', body: `JSON形式が正しくありません: ${error.message}` }
    return
  }

  sending.value = true
  try {
    const response = await fetch(`/api/ai-console/${operation.value}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
    const text = await response.text()
    let formatted = text
    try {
      formatted = JSON.stringify(JSON.parse(text), null, 2)
    } catch {
      // JSON以外のエラー本文もそのまま表示する。
    }
    result.value = { ok: response.ok, status: response.status, body: formatted }
    if (response.ok && operation.value === 'transactions') {
      emit('transaction-added')
    }
  } catch (error) {
    result.value = { ok: false, status: '接続エラー', body: error.message }
  } finally {
    sending.value = false
  }
}

onBeforeUnmount(() => {
  requestBody.value = ''
  result.value = null
})
</script>

<style scoped>
.ai-console-modal {
  width: min(760px, calc(100vw - 2rem));
  max-height: calc(100vh - 2rem);
  overflow-y: auto;
}

.ai-console-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1rem;
}

.ai-console-header h3 {
  margin: 0;
}

.close-btn {
  border: 0;
  background: transparent;
  color: #555;
  cursor: pointer;
  font-size: 1.8rem;
}

.security-note {
  margin-bottom: 1rem;
  padding: 0.9rem;
  border: 1px solid #b8c4ff;
  border-radius: 10px;
  background: #f3f5ff;
  color: #333;
  font-size: 0.9rem;
}

.security-note p {
  margin: 0.35rem 0 0;
}

.form-row {
  margin-bottom: 1rem;
}

.form-row label {
  display: block;
  margin-bottom: 0.4rem;
  font-weight: 700;
}

select,
textarea {
  width: 100%;
  box-sizing: border-box;
  border: 1px solid #ccc;
  border-radius: 8px;
  padding: 0.7rem;
  background: #fff;
  color: #222;
}

textarea {
  resize: vertical;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  line-height: 1.45;
}

.button-row {
  display: flex;
  justify-content: flex-end;
  gap: 0.7rem;
}

.cancel-btn,
.send-btn {
  border: 0;
  border-radius: 8px;
  padding: 0.7rem 1rem;
  cursor: pointer;
}

.cancel-btn {
  background: #e5e7eb;
  color: #333;
}

.send-btn {
  background: #667eea;
  color: #fff;
}

.send-btn:disabled,
.cancel-btn:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.result-panel {
  margin-top: 1rem;
  border-radius: 8px;
  padding: 0.8rem;
}

.result-panel.success {
  border: 1px solid #86c99b;
  background: #effaf2;
}

.result-panel.error {
  border: 1px solid #e49a9a;
  background: #fff2f2;
}

.result-title {
  margin-bottom: 0.5rem;
  font-weight: 700;
}

.result-panel pre {
  margin: 0;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
