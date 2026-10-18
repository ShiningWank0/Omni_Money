<template>
  <section class="card-reconciliation" aria-label="カード利用額の照合" aria-live="polite">
    <p class="reconciliation-bank">{{ bank.item }} <span>{{ bank.date }}</span></p>
    <p v-if="loading" role="status">照合情報を読み込んでいます…</p>
    <p v-else-if="error" class="reconciliation-unavailable">
      照合情報を取得できませんでした。
      <button type="button" @click="$emit('retry')">再読み込み</button>
    </p>
    <template v-else-if="summary">
      <dl>
        <div><dt>クレカ合計</dt><dd>¥{{ formatExactInteger(summary.total) }}</dd></div>
        <div><dt>銀行取引額</dt><dd>¥{{ formatExactInteger(summary.bankAmount) }}</dd></div>
        <div><dt>差額</dt><dd>¥{{ formatExactInteger(summary.difference) }}</dd></div>
      </dl>
      <p class="reconciliation-status" :class="summary.matched ? 'matched' : 'unmatched'">
        {{ summary.matched ? '一致' : '差額あり' }}
      </p>
      <p v-if="summary.hasConflicts" class="reconciliation-conflict">複数の銀行取引に紐付いたクレカ取引があります。</p>
    </template>
  </section>
</template>

<script setup>
import { computed } from 'vue'
import { formatExactInteger } from '../utils/exactAmount.js'
import { reconcileCardPayment } from '../utils/cardReconciliation.js'

const props = defineProps({
  bank: { type: Object, required: true },
  cards: { type: Array, required: true },
  creditCardItems: { type: Array, required: true },
  bankAccountItems: { type: Array, required: true },
  loading: Boolean,
  error: Boolean
})
defineEmits(['retry'])
const summary = computed(() => reconcileCardPayment(props.bank, props.cards, props.creditCardItems, props.bankAccountItems))
</script>

<style scoped>
.card-reconciliation { padding: .75rem; border: 1px solid #d8e7df; border-radius: .65rem; background: #f5faf7; font-size: .85rem; }
.reconciliation-bank { margin: 0 0 .5rem; overflow-wrap: anywhere; }
.reconciliation-bank span { color: #687c74; margin-left: .35rem; }
dl { margin: 0; }
dl div { display: flex; justify-content: space-between; flex-wrap: wrap; gap: .25rem .75rem; }
dt { color: #51685d; }
dd { margin: 0 0 0 auto; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; max-width: 100%; }
.reconciliation-status { margin: .5rem 0 0; font-weight: 700; }
.matched { color: #16724f; }
.unmatched, .reconciliation-conflict { color: #845e17; }
.reconciliation-conflict { margin: .5rem 0 0; }
.reconciliation-unavailable { margin: 0; }
button { border: 0; background: transparent; color: #176e56; text-decoration: underline; cursor: pointer; }
</style>
