import test from 'node:test'
import assert from 'node:assert/strict'
import { reconcileCardPayment } from '../src/utils/cardReconciliation.js'

const bank = { id: 1, account: 'bank', type: 'expense', amount: 450 }
const cards = [
  { id: 2, account: 'card', type: 'expense', amount: 300 },
  { id: 3, account: 'card', type: 'expense', amount: 200 },
  { id: 4, account: 'card', type: 'income', amount: 50 }
]

test('card payment totals subtract refunds and compare without blocking a mismatch', () => {
  const matched = reconcileCardPayment(bank, cards, ['card'], ['bank'])
  assert.equal(matched.total, 450n)
  assert.equal(matched.matched, true)
  const different = reconcileCardPayment({ ...bank, amount: 451 }, cards, ['card'], ['bank'])
  assert.equal(different.difference, -1n)
  assert.equal(different.matched, false)
})

test('sums exact archived values beyond JavaScript and int64 ranges', () => {
  const largeCards = [2, 3].map(id => ({ id, account: 'card', type: 'expense', amount: 1, amount_exact: '9223372036854775807' }))
  const result = reconcileCardPayment(bank, largeCards, ['card'], ['bank'])
  assert.equal(result.total, 18446744073709551614n)
  assert.equal(result.difference, 18446744073709551164n)
})

test('the reconciliation is not applied to other or ambiguously classified fund items', () => {
  assert.equal(reconcileCardPayment({ ...bank, account: 'cash' }, cards, ['card'], ['bank']), null)
  assert.equal(reconcileCardPayment(bank, cards, ['card', 'bank'], ['bank']), null)
  const result = reconcileCardPayment(bank, [...cards, { id: 5, account: 'cash', type: 'expense', amount: 999 }], ['card'], ['bank'])
  assert.equal(result.total, 450n)
})

test('duplicates are not counted twice and historical multi-bank links are reported', () => {
  const conflicted = { ...cards[0], linked_bank_ids: [1, 9] }
  const result = reconcileCardPayment(bank, [conflicted, ...cards], ['card'], ['bank'])
  assert.equal(result.count, 3)
  assert.equal(result.total, 450n)
  assert.equal(result.hasConflicts, true)
})
