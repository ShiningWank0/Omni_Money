import assert from 'node:assert/strict'
import test from 'node:test'

import { exactInteger, formatExactCurrency, formatExactInteger } from '../src/utils/exactAmount.js'

test('exact decimal companion wins over an unsafe JSON number', () => {
  const roundedLegacyNumber = Number('9223372036854775807')
  assert.equal(exactInteger(roundedLegacyNumber, '9223372036854775807'), 9223372036854775807n)
  assert.equal(formatExactInteger(roundedLegacyNumber, '9223372036854775807'), '9,223,372,036,854,775,807')
  assert.equal(formatExactCurrency(-1, '-9223372036854775808'), '¥-9,223,372,036,854,775,808')
})

test('legacy safe numeric responses remain compatible', () => {
  assert.equal(exactInteger(1234), 1234n)
  assert.equal(formatExactCurrency(1234), '¥1,234')
  assert.equal(exactInteger(Number.MAX_SAFE_INTEGER + 1), 0n)
})
