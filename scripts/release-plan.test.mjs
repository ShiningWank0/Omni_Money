import assert from 'node:assert/strict'
import test from 'node:test'
import { releasePlan } from './release-plan.mjs'
test('VERSION releases both products, ordinary main changes publish neither', () => {
  assert.deepEqual(releasePlan('push', ['VERSION', 'frontend/src/App.vue']), { desktop: true, docker: true })
  assert.deepEqual(releasePlan('push', ['frontend/src/App.vue']), { desktop: false, docker: false })
})
test('PR license and orchestration changes validate both product builds', () => {
  for (const path of ['licenses/approved.json', 'frontend/src/generated/thirdPartyNotices.json', 'scripts/oss-notices.mjs', '.github/workflows/ci.yml']) {
    assert.deepEqual(releasePlan('pull_request', [path]), { desktop: true, docker: true })
  }
  assert.deepEqual(releasePlan('pull_request', ['docs/how-to-use.md']), { desktop: false, docker: false })
})
test('manual Docker rehearsals are opt-in; scheduled scans never publish', () => {
  assert.deepEqual(releasePlan('workflow_dispatch', [], true), { desktop: false, docker: true })
  assert.deepEqual(releasePlan('workflow_dispatch', [], false), { desktop: false, docker: false })
  assert.deepEqual(releasePlan('schedule', ['VERSION']), { desktop: false, docker: false })
  assert.throws(() => releasePlan('release', []), /Unexpected/)
})
