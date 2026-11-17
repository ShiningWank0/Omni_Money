import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { checkLinuxEnvironments, workflowPaths } from './linux-environments.mjs'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'omni-linux-lock-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const path of ['build/locks/linux-environments.json', 'Dockerfile', ...workflowPaths]) {
    mkdirSync(dirname(join(root, path)), { recursive: true })
    writeFileSync(join(root, path), readFileSync(new URL(`../${path}`, import.meta.url)))
  }
  return root
}

test('all normal Linux CI/release inputs match one immutable lock', () => checkLinuxEnvironments())
test('a workflow-only digest change is rejected instead of silently changing the environment', t => {
  const root = fixture(t), path = join(root, workflowPaths[0])
  writeFileSync(path, readFileSync(path, 'utf8').replace(/sha256:[a-f0-9]{64}/g, `sha256:${'f'.repeat(64)}`))
  assert.throws(() => checkLinuxEnvironments(root), /differs from reviewed lock/)
})
test('moving OS package resolution cannot return to an ordinary Docker build', t => {
  const root = fixture(t), path = join(root, 'Dockerfile')
  writeFileSync(path, readFileSync(path, 'utf8') + '\nRUN apk add openssl-dev\n')
  assert.throws(() => checkLinuxEnvironments(root), /must not resolve OS packages again/)
})
