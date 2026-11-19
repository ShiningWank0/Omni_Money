import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { capturePaths, checkLinuxEnvironments, workflowPaths } from './linux-environments.mjs'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'omni-linux-lock-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const path of ['build/locks/linux-environments.json', 'Dockerfile', ...workflowPaths, ...capturePaths]) {
    mkdirSync(dirname(join(root, path)), { recursive: true })
    writeFileSync(join(root, path), readFileSync(new URL(`../${path}`, import.meta.url)))
  }
  return root
}

test('all normal Linux CI/release inputs match one immutable lock', () => checkLinuxEnvironments())
test('major updates cannot delete the capture workflow or its source recipe', t => {
  for (const path of ['.github/workflows/capture-build-environments.yml', 'build/environments/ubuntu.Dockerfile']) {
    const root = fixture(t)
    rmSync(join(root, path))
    assert.throws(() => checkLinuxEnvironments(root), error => error.code === 'ENOENT' && error.path === join(root, path))
  }
})
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

test('job containers cannot silently lose orphan reaping', t => {
  const root = fixture(t), path = join(root, '.github/workflows/release-desktop.yml')
  writeFileSync(path, readFileSync(path, 'utf8').replace('--init ', ''))
  assert.throws(() => checkLinuxEnvironments(root), /must reap orphaned child processes/)
})

test('OCI export allocates a writable path from the container environment', t => {
  const root = fixture(t), temp = join(root, 'container-temp'), output = join(root, 'outputs')
  mkdirSync(temp)
  const workflow = readFileSync(join(root, '.github/workflows/release-docker.yml'), 'utf8')
  const setup = workflow.match(/id: oci-output\n\s+run: \|\n([\s\S]*?)\n\s+- name:/)?.[1]
  assert.ok(setup, 'OCI export path setup is missing')
  const result = spawnSync('bash', ['-c', setup], {
    env: { ...process.env, RUNNER_TEMP: temp, GITHUB_OUTPUT: output }, encoding: 'utf8',
  })
  assert.equal(result.status, 0, result.stderr)
  const path = readFileSync(output, 'utf8').trim().replace(/^path=/, '')
  assert.ok(path.startsWith(temp + '/'))
  writeFileSync(path, 'OCI fixture')
  assert.match(workflow, /outputs: type=oci,dest=\$\{\{ steps\.oci-output\.outputs\.path \}\}/)
})
