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
        </div>
        <div class="modal-buttons" style="display: flex; justify-content: space-between; align-items: center;">
          <div>
            <button v-if="isEditMode" type="button" class="delete-btn" @click="$emit('delete')">削除</button>
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
import { ref, computed, onMounted } from 'vue'

const props = defineProps({
  isEditMode: Boolean,
  transaction: Object,
  fundItems: { type: Array, default: () => [] },
  itemNames: { type: Array, default: () => [] }
})

const emit = defineEmits(['save', 'delete', 'close'])

const showFundItemDropdown = ref(false)

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

function onAmountInput(e) {
  // 数値以外を除去
  form.value.amount = e.target.value.replace(/[^0-9]/g, '')
}

function handleSubmit() {
  const amount = parseInt(form.value.amount)
  if (!amount || amount <= 0) {
    alert('金額は正の数値である必要があります')
    return
  }

  emit('save', {
    account: form.value.fundItem,
    date: form.value.date,
    time: form.value.time,
    item: form.value.item,
    type: form.value.type,
    amount: amount,
    memo: form.value.memo
  })
}

onMounted(() => {
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
  }
})
</script>
