export function exactInteger(value, exactValue) {
  const candidate = exactValue ?? value ?? 0
  if (typeof candidate === 'bigint') return candidate
  if (typeof candidate === 'string' && /^-?\d+$/.test(candidate)) return BigInt(candidate)
  if (typeof candidate === 'number' && Number.isSafeInteger(candidate)) return BigInt(candidate)
  return BigInt(0)
}

export function formatExactInteger(value, exactValue) {
  return exactInteger(value, exactValue).toLocaleString('ja-JP')
}

export function formatExactCurrency(value, exactValue) {
  return '¥' + formatExactInteger(value, exactValue)
}
