// HTTP errors carry metadata without authorizing automatic mutation retries.
export class ApiError extends Error {
  constructor(message, { status = 0, code = 'request_failed', cause, definitiveResponse } = {}) {
    super(message, { cause })
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryable = [429, 502, 503, 504].includes(status) || code === 'network_error'
    if (definitiveResponse !== undefined) this.definitiveResponse = definitiveResponse
  }
}
export const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)
export const isString = value => typeof value === 'string'
export const isCount = value => Number.isSafeInteger(value) && value >= 0
const isID = value => Number.isSafeInteger(value) && value > 0
const arrayOf = predicate => value => Array.isArray(value) && value.every(predicate)
const strings = arrayOf(isString)
const record = value => isObject(value) && !Object.hasOwn(value, 'error')
const identified = value => record(value) && (isID(value.id) || (isString(value.id) && value.id.length > 0))
const transaction = value => identified(value) && isString(value.date) && isString(value.item) && ['income', 'expense'].includes(value.type) && Number.isFinite(value.amount)
const tag = value => identified(value) && isString(value.name) && (value.children == null || arrayOf(tag)(value.children))
export const schema = {
  object: record,
  strings,
  transactions: arrayOf(transaction),
  transaction,
  transactionResult: value => record(value) && transaction(value.transaction),
  tags: arrayOf(tag), tag,
  records: arrayOf(identified),
  auth: value => record(value) && typeof value.authenticated === 'boolean',
  authenticated: value => record(value) && value.authenticated === true,
  success: value => record(value) && value.success === true,
  message: value => record(value) && isString(value.message),
  user: value => record(value) && identified(value.user),
  invitation: value => record(value) && identified(value.invitation) && isString(value.token) && value.token.length > 0,
  passwordReset: value => record(value) && identified(value.password_reset),
  resetToken: value => record(value) && identified(value.password_reset) && isString(value.token) && value.token.length > 0,
  passkey: value => record(value) && identified(value.passkey),
  ceremony: value => record(value) && isString(value.ceremony_id) && record(value.options),
  image: value => identified(value) && isString(value.filename),
  images: value => record(value) && (value.images === null || arrayOf(item => identified(item) && isString(item.filename))(value.images)) && (value.next_cursor == null || isString(value.next_cursor)),
  balance: value => record(value) && (value.accounts === null || strings(value.accounts)) && (value.dates === null || strings(value.dates)) && record(value.balances) && (value.accounts ?? []).every(account => Array.isArray(value.balances[account]) && value.balances[account].length === (value.dates ?? []).length && value.balances[account].every(Number.isFinite)),
  impact: value => record(value) && isID(value.tag_id) && isString(value.tag_name) && isCount(value.descendant_count) && isCount(value.transaction_count),
  summary: arrayOf(value => record(value) && Number.isInteger(value.tag_id) && isString(value.tag_name) && Number.isFinite(value.amount) && isCount(value.count)),
  snapshot: value => record(value) && isString(value.path) && value.path.length > 0,
  imported: value => record(value) && isCount(value.imported_count)
}
export function responseError(response, data, fallback = 'リクエストに失敗しました') {
  const codes = { 401: 'authentication_required', 403: 'forbidden', 409: 'conflict', 428: 'recent_auth_required', 429: 'rate_limited', 503: 'service_unavailable' }
  const error = new ApiError(isString(data?.error) ? data.error : fallback, {
    status: response.status,
    code: isString(data?.code) ? data.code : (codes[response.status] || 'request_failed')
  })
  error.loginRequired = data?.login_required === true
  return error
}
export async function checkStatus(response, fallback) {
  if (response.ok) return
  let data
  try { data = await response.json() } catch { /* non-JSON proxy failure */ }
  throw responseError(response, data, fallback)
}
export function validateData(data, validate, response) {
  if (!validate(data)) throw new ApiError('APIの応答が不正です', { status: response.status, code: 'invalid_response' })
  return data
}
export async function expectJSON(response, validate = schema.object, fallback) {
  await checkStatus(response, fallback)
  let data
  try { data = await response.json() } catch (cause) {
    throw new ApiError('APIのJSON応答が不正です', { status: response.status, code: 'invalid_response', cause })
  }
  return validateData(data, validate, response)
}
// Some legacy Go list endpoints encode a nil slice as JSON null.
export async function expectList(response, validate) {
  const data = await expectJSON(response, value => value === null || validate(value))
  return data ?? []
}
export async function expectVoid(response, validate = schema.message) {
  await checkStatus(response)
  if (response.status === 204) return
  await expectJSON(response, validate)
}
