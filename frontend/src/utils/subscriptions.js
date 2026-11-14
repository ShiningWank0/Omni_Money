export const subscriptionCurrencies = ['JPY', 'USD', 'EUR', 'GBP', 'AUD', 'CAD', 'CHF', 'CNY', 'HKD', 'SGD']

export function localDate(now = new Date()) {
  return `${String(now.getFullYear()).padStart(4, '0')}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

export function scheduleDate(year, month, day) {
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]
  return `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(Math.min(day, days)).padStart(2, '0')}`
}

export function nextSubscriptionDate(date, interval, day, anchorMonth) {
  let [year, month] = date.slice(0, 10).split('-').map(Number)
  if (interval === 'yearly') { year++; month = anchorMonth }
  else if (++month > 12) { month = 1; year++ }
  return scheduleDate(year, month, day)
}

export function initialSubscription(transaction, interval = 'monthly', today = localDate()) {
  const date = transaction.date.slice(0, 10)
  const [, month, day] = date.split('-').map(Number)
  // Skip past dates efficiently without replaying historical payments.
  const [yearNow, monthNow] = (date > today ? date : today).split('-').map(Number)
  let next = scheduleDate(yearNow, interval === 'yearly' ? month : monthNow, day)
  if (next <= date || next < today) next = nextSubscriptionDate(next, interval, day, month)
  return {
    source_transaction_id: transaction.id, revision: 0, account: transaction.account || transaction.fundItem,
    item: transaction.item, memo: transaction.memo || '', currency: 'JPY',
    original_amount: transaction.amount_exact || String(transaction.amount), interval, anchor_day: day,
    anchor_month: month, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Tokyo',
    next_date: next, enabled: true, tags: (transaction.tags || []).map(tag => tag.id)
  }
}

export function calendarCells(month) {
  if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(month)) return []
  const [year, monthNumber] = month.split('-').map(Number)
  if (year < 1 || year > 9998) return []
  const date = new Date(0)
  date.setUTCFullYear(year, monthNumber - 1, 1)
  date.setUTCHours(0, 0, 0, 0)
  const offset = date.getUTCDay()
  const days = Number(scheduleDate(year, monthNumber, 31).slice(-2))
  return Array.from({ length: Math.ceil((offset + days) / 7) * 7 }, (_, index) => {
    const day = index - offset + 1
    return day < 1 || day > days ? null : scheduleDate(year, monthNumber, day)
  })
}
