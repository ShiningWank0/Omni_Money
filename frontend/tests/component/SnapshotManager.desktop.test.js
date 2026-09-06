import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listSnapshots: vi.fn(),
  restoreSnapshot: vi.fn(),
  clearSessionSecrets: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({
  ...api,
  isWailsMode: true
}))

import SnapshotManager from '../../src/components/SnapshotManager.vue'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function startRestore(pending, emitted) {
  api.restoreSnapshot.mockReturnValueOnce(pending.promise)
  const wrapper = mount(SnapshotManager, {
    attrs: {
      onRestored: value => emitted.push(['restored', value]),
      onClose: () => emitted.push(['close'])
    }
  })
  await flushPromises()
  await wrapper.get('.restore-btn').trigger('click')
  await wrapper.get('.confirm-yes-btn').trigger('click')
  await Promise.resolve()
  return wrapper
}

describe('SnapshotManager Desktop restore', () => {
  beforeEach(() => {
    api.listSnapshots.mockResolvedValue(['omni_money_20260102_030405.db'])
  })

  for (const failed of [false, true]) {
    it(`emits restored before close after a ${failed ? 'failed' : 'successful'} restore`, async () => {
      const pending = deferred()
      const emitted = []
      const wrapper = await startRestore(pending, emitted)

      expect(wrapper.get('.close-btn').attributes('disabled')).toBeDefined()
      await wrapper.get('.modal-overlay').trigger('click')
      await wrapper.get('.close-btn').trigger('click')
      expect(emitted).toEqual([])

      if (failed) pending.reject(new Error('restore failed'))
      else pending.resolve()
      await flushPromises()

      expect(api.clearSessionSecrets).toHaveBeenCalledTimes(failed ? 1 : 0)
      expect(emitted).toEqual(failed
        ? [['restored', { failed: true }], ['close']]
        : [['restored', undefined], ['close']])
      expect(wrapper.text()).toContain('スナップショットはありません')
      expect(wrapper.get('.close-btn').attributes('disabled')).toBeUndefined()
    })
  }
})
