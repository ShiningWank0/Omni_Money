import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import test from 'node:test'

import {
  clearSnapshotRestoreMarker,
  executeSnapshotRestore
} from '../src/utils/snapshotRestore.js'

const snapshotManagerSource = readFileSync(
  resolve(import.meta.dirname, '../src/components/SnapshotManager.vue'),
  'utf8'
)
const appSource = readFileSync(resolve(import.meta.dirname, '../src/App.vue'), 'utf8')

function deferred() {
  let resolvePromise
  let rejectPromise
  const promise = new Promise((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return { promise, resolve: resolvePromise, reject: rejectPromise }
}

function withLocalStorage(value, callback) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    ...(typeof value === 'function' ? { get: value } : { value, writable: true })
  })
  try {
    return callback()
  } finally {
    if (descriptor) {
      Object.defineProperty(globalThis, 'localStorage', descriptor)
    } else {
      delete globalThis.localStorage
    }
  }
}

test('desktop restore completion survives a pending resolve or reject', async () => {
  for (const failed of [false, true]) {
    const pending = deferred()
    const events = []
    const restore = executeSnapshotRestore({
      name: 'snapshot.db',
      restore: name => {
        events.push(['restore', name])
        return pending.promise
      },
      isDesktop: true,
      setRestoring: value => events.push(['restoring', value]),
      clearMessage: () => events.push(['message']),
      clearSecrets: () => events.push(['secrets']),
      purgeState: () => events.push(['purge']),
      clearMarker: () => events.push(['marker']),
      emit: (...event) => events.push(event),
      createSessionExpiredEvent: () => { throw new Error('desktop must not expire a server session') },
      dispatchSessionExpired: () => { throw new Error('desktop must not dispatch a server session event') },
      isOnLoginPage: () => false,
      redirectToLogin: () => { throw new Error('desktop must not redirect') }
    })

    await Promise.resolve()
    assert.deepEqual(events, [
      ['restoring', true],
      ['message'],
      ['restore', 'snapshot.db']
    ])

    if (failed) pending.reject(new Error('restore failed'))
    else pending.resolve()
    await restore

    assert.deepEqual(events, failed
      ? [
          ['restoring', true],
          ['message'],
          ['restore', 'snapshot.db'],
          ['secrets'],
          ['purge'],
          ['restoring', false],
          ['restored', { failed: true }],
          ['close']
        ]
      : [
          ['restoring', true],
          ['message'],
          ['restore', 'snapshot.db'],
          ['purge'],
          ['restoring', false],
          ['restored'],
          ['close']
        ])
  }
})

test('server restore purges secrets, survives marker cleanup failure, and expires in order', async () => {
  for (const { failed, preventDefault } of [
    { failed: false, preventDefault: false },
    { failed: true, preventDefault: true }
  ]) {
    const events = []
    const event = { defaultPrevented: preventDefault }
    const restore = executeSnapshotRestore({
      name: 'snapshot.db',
      restore: failed ? async () => { throw new Error('restore failed') } : async () => {},
      isDesktop: false,
      setRestoring: value => events.push(['restoring', value]),
      clearMessage: () => events.push(['message']),
      clearSecrets: () => events.push(['secrets']),
      purgeState: () => events.push(['purge']),
      clearMarker: () => {
        events.push(['marker'])
        throw new Error('storage denied')
      },
      emit: (...emitted) => events.push(emitted),
      createSessionExpiredEvent: reason => {
        events.push(['event:create', reason])
        return event
      },
      dispatchSessionExpired: dispatched => {
        assert.equal(dispatched, event)
        events.push(['event:dispatch'])
      },
      isOnLoginPage: () => false,
      redirectToLogin: reason => events.push(['redirect', reason])
    })
    await restore

    const reason = failed ? 'snapshot-restore-failed' : 'snapshot-restored'
    const expected = [
      ['restoring', true],
      ['message'],
      ['secrets'],
      ['marker'],
      ['purge'],
      ['restoring', false],
      ['close'],
      ['event:create', reason],
      ['event:dispatch']
    ]
    if (!preventDefault) expected.push(['redirect', reason])
    assert.deepEqual(events, expected)
  }
})

test('snapshot restore marker cleanup survives a throwing localStorage getter', () => {
  let cleanupResult
  assert.doesNotThrow(() => {
    cleanupResult = withLocalStorage(() => { throw new Error('storage denied') }, () =>
      clearSnapshotRestoreMarker()
    )
  })
  assert.equal(cleanupResult, false)
})

test('snapshot restore marker cleanup survives a throwing removeItem', () => {
  const storage = { removeItem() { throw new Error('storage denied') } }
  assert.equal(clearSnapshotRestoreMarker(storage), false)
})

test('snapshot restore source contracts keep close guarded and post-restore wiring intact', () => {
  assert.equal((snapshotManagerSource.match(/@click="closeSnapshotManager"/g) || []).length, 2)
  assert.match(snapshotManagerSource, /class="close-btn" @click="closeSnapshotManager" :disabled="isRestoring"/)
  assert.match(
    snapshotManagerSource,
    /function closeSnapshotManager\(\) \{\s*if \(isRestoring\.value\) return\s*emit\('close'\)/
  )
  assert.doesNotMatch(snapshotManagerSource, /@click="\$emit\('close'\)"/)
  assert.match(snapshotManagerSource, /return executeSnapshotRestore\(\{\s*name,/)
  assert.match(snapshotManagerSource, /restore: apiRestoreSnapshot/)
  assert.match(appSource, /@restored="handleSnapshotRestored"/)

  const handlerStart = appSource.indexOf('async function handleSnapshotRestored()')
  const handlerEnd = appSource.indexOf('\n}\n', handlerStart) + 2
  const handler = appSource.slice(handlerStart, handlerEnd)
  assert.ok(handler.indexOf('await lockDesktopVaultNow()') < handler.indexOf('window.location.reload()'))
})
