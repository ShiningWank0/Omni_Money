import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listSnapshots: vi.fn(),
  restoreSnapshot: vi.fn(),
  clearSessionSecrets: vi.fn()
}))

vi.mock('../../src/utils/api', () => ({
  ...api,
  isWailsMode: false
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

async function startRestore(pending) {
  api.restoreSnapshot.mockReturnValueOnce(pending.promise)
  const wrapper = mount(SnapshotManager)
  await flushPromises()
  await wrapper.get('.restore-btn').trigger('click')
  await wrapper.get('.confirm-yes-btn').trigger('click')
  await Promise.resolve()
  return wrapper
}

describe('SnapshotManager server restore', () => {
  beforeEach(() => {
    api.listSnapshots.mockResolvedValue(['omni_money_20260102_030405.db'])
  })

  for (const failed of [false, true]) {
    it(`${failed ? 'failed' : 'successful'} restore blocks close and expires the session`, async () => {
      const pending = deferred()
      const events = []
      const onExpired = event => {
        event.preventDefault()
        events.push(event.detail.reason)
      }
      window.addEventListener('omni-money:session-expired', onExpired)

      const wrapper = await startRestore(pending)
      expect(wrapper.get('.close-btn').attributes('disabled')).toBeDefined()
      expect(wrapper.get('.confirm-no-btn').attributes('disabled')).toBeDefined()
      await wrapper.get('.modal-overlay').trigger('click')
      await wrapper.get('.close-btn').trigger('click')
      expect(wrapper.emitted('close')).toBeUndefined()

      if (failed) pending.reject(new Error('restore failed'))
      else pending.resolve()
      await flushPromises()

      expect(api.clearSessionSecrets).toHaveBeenCalledOnce()
      expect(wrapper.emitted('restored')).toBeUndefined()
      expect(wrapper.emitted('close')).toHaveLength(1)
      expect(events).toEqual([failed ? 'snapshot-restore-failed' : 'snapshot-restored'])
      expect(wrapper.text()).toContain('スナップショットはありません')
      expect(wrapper.get('.close-btn').attributes('disabled')).toBeUndefined()
    })
  }
})
