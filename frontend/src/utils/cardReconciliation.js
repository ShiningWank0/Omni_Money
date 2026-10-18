import { exactInteger } from './exactAmount.js'

export function transactionAccount(transaction) {
  return String(transaction?.account ?? transaction?.fundItem ?? '').trim()
}

export function ledgerAccountRole(account, creditCardItems, bankAccountItems) {
  const normalized = String(account ?? '').trim()
  const card = creditCardItems.some(item => item.trim() === normalized)
  const bank = bankAccountItems.some(item => item.trim() === normalized)
  if (card && !bank) return 'card'
  if (bank && !card) return 'bank'
  return ''
}

// この合計・差額計算は登録済みクレカと銀行口座の紐付け専用です。
// 将来ほかの資金項目同士を紐付けできても、その関係へ自動適用しません。
// 金額の不一致は表示情報であり、紐付けや保存を拒否する条件にしません。
export function reconcileCardPayment(bank, cards, creditCardItems, bankAccountItems) {
  const cardAccounts = new Set(creditCardItems.map(item => item.trim()))
  const bankAccounts = new Set(bankAccountItems.map(item => item.trim()))
  const bankAccount = transactionAccount(bank)
  if (!bankAccounts.has(bankAccount) || cardAccounts.has(bankAccount)) return null
  const seen = new Set()
  let total = BigInt(0)
  let hasConflicts = false
  for (const card of cards) {
    const account = transactionAccount(card)
    if (!cardAccounts.has(account) || bankAccounts.has(account) || seen.has(card.id)) continue
    seen.add(card.id)
    const amount = exactInteger(card.amount, card.amount_exact)
    total += card.type === 'income' ? -amount : amount
    if (new Set(card.linked_bank_ids ?? []).size > 1) hasConflicts = true
  }
  const amount = exactInteger(bank.amount, bank.amount_exact)
  const bankAmount = bank.type === 'income' ? -amount : amount
  const difference = total - bankAmount
  return { total, bankAmount, difference, matched: difference === BigInt(0), count: seen.size, hasConflicts }
}
