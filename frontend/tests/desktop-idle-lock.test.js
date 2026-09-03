import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DESKTOP_IDLE_DEFAULT_MINUTES,
  DESKTOP_IDLE_STORAGE_KEY,
  createDesktopIdleLock,
  createDesktopVaultLockRequest,
  isValidDesktopIdleMinutes,
  loadDesktopIdleMinutes,
  saveDesktopIdleMinutes
} from '../src/utils/desktopIdleLock.js'

function fakeDocument() {
  const listeners = new Map()
  return {
    visibilityState: 'visible',
    addEventListener(type, listener) { listeners.set(type, listener) },
    removeEventListener(type) { listeners.delete(type) },
    emit(type, event = {}) { listeners.get(type)?.({ type, ...event }) }
  }
}

function harness() {
  const document = fakeDocument()
  let monotonic = 1000
  let wall = 10000
  let nextTimer = 0
  const timers = new Map()
  const curtains = []
  const expired = []
  const controller = createDesktopIdleLock({
    document,
    performanceNow: () => monotonic,
    wallNow: () => wall,
    setTimer(callback, delay) {
      const id = ++nextTimer
      timers.set(id, { callback, delay })
      return id
    },
    clearTimer: id => timers.delete(id),
    onCurtainChange: visible => curtains.push(visible),
    onExpired: reason => expired.push(reason)
  })
  return {
    controller,
    document,
    curtains,
    expired,
    timers,
    advance(performanceMs, wallMs = performanceMs) {
      monotonic += performanceMs
      wall += wallMs
    }
  }
}

test('desktop idle preference accepts only integer minutes from 5 through 120', () => {
  for (const value of [5, 15, 120]) assert.equal(isValidDesktopIdleMinutes(value), true)
  for (const value of [4, 121, 5.5, '15', null]) assert.equal(isValidDesktopIdleMinutes(value), false)
})

test('preference is versioned and invalid or unreadable storage falls back to 15', () => {
  const values = new Map()
  const storage = {
    getItem: key => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value)
  }
  assert.equal(loadDesktopIdleMinutes(storage), DESKTOP_IDLE_DEFAULT_MINUTES)
  assert.equal(saveDesktopIdleMinutes(30, storage), true)
  assert.equal(JSON.parse(values.get(DESKTOP_IDLE_STORAGE_KEY)).version, 1)
  assert.equal(loadDesktopIdleMinutes(storage), 30)
  for (const invalid of ['null', '{', '{"version":2,"minutes":30}', '{"version":1,"minutes":4}']) {
    values.set(DESKTOP_IDLE_STORAGE_KEY, invalid)
    assert.equal(loadDesktopIdleMinutes(storage), DESKTOP_IDLE_DEFAULT_MINUTES)
  }
  const unavailable = { getItem() { throw new Error('denied') }, setItem() { throw new Error('denied') } }
  assert.equal(loadDesktopIdleMinutes(unavailable), DESKTOP_IDLE_DEFAULT_MINUTES)
  assert.equal(saveDesktopIdleMinutes(15, unavailable), false)
})

test('only visible trusted allowed events refresh activity and movement is leading-throttled', () => {
  const h = harness()
  h.controller.start(5)
  h.advance(4 * 60 * 1000)
  h.document.emit('pointerdown', { isTrusted: false })
  h.advance(60 * 1000)
  assert.equal(h.controller.checkExpiry(), true)

  const visible = harness()
  visible.controller.start(5)
  visible.advance(4 * 60 * 1000)
  visible.document.emit('pointermove', { isTrusted: true })
  visible.advance(500)
  visible.document.emit('wheel', { isTrusted: true })
  visible.advance(4 * 60 * 1000 + 501)
  assert.equal(visible.controller.checkExpiry(), false)
  visible.advance(60 * 1000)
  assert.equal(visible.controller.checkExpiry(), true)

  const hidden = harness()
  hidden.controller.start(5)
  hidden.document.visibilityState = 'hidden'
  hidden.advance(4 * 60 * 1000)
  hidden.document.emit('keydown', { isTrusted: true })
  hidden.advance(60 * 1000)
  assert.equal(hidden.controller.checkExpiry(), true)
})

test('the greater non-negative clock elapsed prevents rollback extension and catches sleep', () => {
  const rollback = harness()
  rollback.controller.start(5)
  rollback.advance(5 * 60 * 1000, -60 * 60 * 1000)
  assert.equal(rollback.controller.checkExpiry(), true)

  const sleep = harness()
  sleep.controller.start(5)
  sleep.advance(1000, 5 * 60 * 1000)
  assert.equal(sleep.controller.checkExpiry(), true)
})

test('an expired first click locks before it can refresh activity', () => {
  const h = harness()
  h.controller.start(5)
  h.advance(5 * 60 * 1000)
  h.document.emit('pointerdown', { isTrusted: true })
  assert.deepEqual(h.expired, ['idle'])
})

test('hidden content is curtained and visible restore uncovers only before expiry', () => {
  const within = harness()
  within.controller.start(5)
  within.document.visibilityState = 'hidden'
  within.document.emit('visibilitychange')
  within.advance(4 * 60 * 1000)
  within.document.visibilityState = 'visible'
  within.document.emit('visibilitychange')
  assert.deepEqual(within.curtains, [true, false])
  assert.deepEqual(within.expired, [])

  const expired = harness()
  expired.controller.start(5)
  expired.document.visibilityState = 'hidden'
  expired.document.emit('visibilitychange')
  expired.advance(5 * 60 * 1000)
  expired.document.visibilityState = 'visible'
  expired.document.emit('visibilitychange')
  assert.deepEqual(expired.curtains, [true])
  assert.deepEqual(expired.expired, ['resume'])

  expired.controller.stop()
  expired.document.visibilityState = 'hidden'
  expired.controller.start(5)
  assert.deepEqual(expired.curtains, [true, true])
})

test('simultaneous desktop lock requests share one ordered lock operation', async () => {
  const order = []
  let releaseTick
  let lockCalls = 0
  const request = createDesktopVaultLockRequest({
    showCurtain: reason => order.push(`curtain:${reason}`),
    invalidateResponses: () => order.push('invalidate'),
    nextTick: () => new Promise(resolve => { releaseTick = resolve }),
    purge: () => order.push('purge'),
    lock: async () => {
      lockCalls++
      order.push('lock')
      return { state: 'locked' }
    },
    setLockedStatus: () => order.push('status'),
    onFailure: error => { throw error },
    onSettled: () => order.push('settled')
  })

  const first = request('idle')
  const second = request('manual')
  assert.equal(first, second)
  assert.deepEqual(order, ['curtain:idle', 'invalidate'])
  releaseTick()
  await first
  assert.equal(lockCalls, 1)
  assert.deepEqual(order, ['curtain:idle', 'invalidate', 'purge', 'lock', 'status', 'settled'])
})
