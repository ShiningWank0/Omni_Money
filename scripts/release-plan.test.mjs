import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync, mkdirSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { root } from './oss-notices.mjs'
import { releasePlan } from './release-plan.mjs'

test('desktop publishing selects only distribution archives from a shared CI run', t => {
  const support = spawnSync('bash', ['-c', 'type mapfile >/dev/null && command -v sha256sum >/dev/null'])
  if (support.status !== 0) { t.skip('release scripts require Bash mapfile and sha256sum'); return }
  const workflow = readFileSync(join(root, '.github/workflows/release-desktop.yml'), 'utf8')
  const names = [...workflow.matchAll(/^\s+artifact_name: ([a-z0-9-]+)\s*$/gm)].map(match => match[1])
  assert.ok(names.length > 0)
  const step = name => {
    const block = workflow.split(`      - name: ${name}\n`)[1]?.split('\n      - name: ')[0]
    assert.ok(block, `missing workflow step ${name}`)
    return block.split('        run: |\n')[1].split('\n').map(line => line.startsWith('          ') ? line.slice(10) : line).join('\n')
  }
  const checksum = step('Generate checksums')
  const publish = step('Create GitHub Release')
  const selection = publish.slice(publish.indexOf("mapfile -d '' RELEASE_FILES"), publish.indexOf('gh release create'))
  assert.ok(selection.includes('RELEASE_FILES'))
  mkdirSync(join(root, '.oss-cache/tmp'), { recursive: true })
  const directory = mkdtempSync(join(root, '.oss-cache/tmp/release-files-'))
  try {
    const expected = names.map(name => `${name}/${name}-v9.9.9${name.includes('linux') ? '.tar.gz' : '.zip'}`)
    for (const path of [...expected, 'oss-license-inventories/manifest.json', 'omni-money-server-e2e-failure/debug.zip', 'digests-linux-amd64/digest']) {
      const file = join(directory, 'dist', path)
      mkdirSync(join(file, '..'), { recursive: true })
      writeFileSync(file, 'fixture')
    }
    const run = source => spawnSync('bash', ['-c', source], { cwd: directory, encoding: 'utf8' })
    const generated = run(checksum)
    assert.equal(generated.status, 0, generated.stderr)
    const checksums = readFileSync(join(directory, 'dist/SHA256SUMS'), 'utf8').trim().split('\n').map(line => line.slice(66)).sort()
    assert.deepEqual(checksums, expected.toSorted())
    const selected = run(`${selection}\nprintf '%s\\n' "\${RELEASE_FILES[@]}"`)
    assert.equal(selected.status, 0, selected.stderr)
    assert.deepEqual(selected.stdout.trim().split('\n').sort(), [...expected.map(path => `dist/${path}`), 'dist/SHA256SUMS'].sort())
    writeFileSync(join(directory, 'dist', names[0], 'duplicate.zip'), 'extra')
    assert.notEqual(run(checksum).status, 0, 'ambiguous release archives must fail')
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
test('VERSION releases both products, ordinary main changes publish neither', () => {
  assert.deepEqual(releasePlan('push', ['VERSION', 'frontend/src/App.vue']), { desktop: true, docker: true })
  assert.deepEqual(releasePlan('push', ['frontend/src/App.vue']), { desktop: false, docker: false })
})
test('PR license and orchestration changes validate both product builds', () => {
  for (const path of ['licenses/approved.json', 'frontend/src/generated/thirdPartyNotices.json', 'scripts/oss-notices.mjs', '.github/workflows/ci.yml', 'scripts/ci_ubuntu_apt.py']) {
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
test('native and development Wails harness changes automatically validate desktop artifacts', () => {
  for (const path of ['scripts/wails-native-integration.mjs', 'scripts/wails-native-ui.py', 'scripts/wails-native-integration.test.mjs', 'scripts/wails-dev-integration.mjs']) {
    assert.deepEqual(releasePlan('pull_request', [path]), { desktop: true, docker: false })
  }
})
