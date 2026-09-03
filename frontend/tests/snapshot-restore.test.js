import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import test from 'node:test'

import {
  clearSnapshotRestoreMarker,
  notifySnapshotRestoreCompletion
} from '../src/utils/snapshotRestore.js'

const snapshotManagerSource = readFileSync(
  resolve(import.meta.dirname, '../src/components/SnapshotManager.vue'),
  'utf8'
)

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

test('restore completion stays observable while a pending restore settles', async () => {
  for (const failed of [false, true]) {
    const events = []
    let restoring = true
    let settle
    const pendingRestore = new Promise((resolve, reject) => {
      settle = failed ? () => reject(new Error('restore failed')) : resolve
    })
    const requestClose = () => {
      if (restoring) return
      events.push(['close'])
    }

    requestClose()
    assert.deepEqual(events, [])

    const settled = pendingRestore.then(
      () => {
        restoring = false
        notifySnapshotRestoreCompletion((...event) => events.push(event))
      },
      () => {
        restoring = false
        notifySnapshotRestoreCompletion((...event) => events.push(event), true)
      }
    )
    settle()
    await settled

    assert.deepEqual(events, failed
      ? [['restored', { failed: true }], ['close']]
      : [['restored'], ['close']])
  }
})

test('restore UI cannot close while restoration is pending', () => {
  assert.match(snapshotManagerSource, /class="modal-overlay" @click="closeSnapshotManager"/)
  assert.match(snapshotManagerSource, /class="close-btn" @click="closeSnapshotManager" :disabled="isRestoring"/)
  assert.match(
    snapshotManagerSource,
    /function closeSnapshotManager\(\) \{\s*if \(isRestoring\.value\) return\s*emit\('close'\)/
  )
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

test('snapshot restore marker cleanup removes the marker when storage is available', () => {
  const removed = []
  assert.equal(clearSnapshotRestoreMarker({ removeItem: key => removed.push(key) }), true)
  assert.deepEqual(removed, ['snapshot_restored'])
})
