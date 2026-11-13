import assert from 'node:assert/strict'
import test from 'node:test'
import { calendarCells, initialSubscription, nextSubscriptionDate, scheduleDate } from '../src/utils/subscriptions.js'

test('monthly/yearly schedules retain the anchor through short months and leap years', () => {
  assert.equal(nextSubscriptionDate('2026-01-31', 'monthly', 31, 1), '2026-02-28')
  assert.equal(nextSubscriptionDate('2026-02-28', 'monthly', 31, 1), '2026-03-31')
  assert.equal(nextSubscriptionDate('2027-02-28', 'yearly', 29, 2), '2028-02-29')
  assert.equal(nextSubscriptionDate('2028-02-29', 'yearly', 29, 2), '2029-02-28')
  assert.equal(scheduleDate(2100, 2, 29), '2100-02-28')
})

test('new subscriptions skip historical payments without mutating the source', () => {
  const transaction = { id: 1, date: '2026-01-31', account: 'card', item: 'Music', amount: 1000, amount_exact: '1000', tags: [{ id: 3 }] }
  const rule = initialSubscription(transaction, 'monthly', '2026-11-13')
  assert.equal(rule.next_date, '2026-11-30')
  assert.equal(rule.anchor_day, 31)
  assert.equal(rule.source_transaction_id, 1)
  assert.deepEqual(rule.tags, [3])
  assert.equal(transaction.subscription, undefined)
  assert.equal(initialSubscription({ ...transaction, date: '2027-01-31' }, 'monthly', '2026-11-13').next_date, '2027-02-28')
  assert.equal(initialSubscription(transaction, 'yearly', '2026-11-13').next_date, '2027-01-31')
})

test('calendar supports far past/future months and rejects malformed input', () => {
  for (const month of ['0001-01', '2028-02', '2100-02', '9998-12']) {
    const cells = calendarCells(month)
    assert.equal(cells.length % 7, 0)
    assert.equal(cells.filter(Boolean)[0], `${month}-01`)
    assert.equal(new Set(cells.filter(Boolean)).size, cells.filter(Boolean).length)
  }
  assert.equal(calendarCells('2028-02').filter(Boolean).length, 29)
  assert.equal(calendarCells('2100-02').filter(Boolean).length, 28)
  for (const invalid of ['', '0000-01', '9999-01', '2026-13', '2026-1', '<script>']) assert.deepEqual(calendarCells(invalid), [])
})
