import assert from 'node:assert/strict'
import test from 'node:test'

import { formatTagDeleteImpact, syncTagDrafts } from '../src/utils/tagManagerSafety.js'

test('tag drafts follow authoritative rename and remove deleted IDs', () => {
  const oldDrafts = syncTagDrafts([{ id: 1, name: 'old', children: [{ id: 2, name: 'child' }] }])
  assert.deepEqual(oldDrafts, { 1: 'old', 2: 'child' })
  const freshDrafts = syncTagDrafts([{ id: 1, name: 'renamed', children: [] }])
  assert.deepEqual(freshDrafts, { 1: 'renamed' })
  assert.equal(freshDrafts[2], undefined)
})

test('delete confirmation states cascading impact', () => {
  assert.match(formatTagDeleteImpact({ tag_name: '旅行', descendant_count: 2, transaction_count: 7 }), /子タグ 2件/)
  assert.match(formatTagDeleteImpact({ tag_name: '旅行', descendant_count: 2, transaction_count: 7 }), /紐付いた取引 7件/)
})
