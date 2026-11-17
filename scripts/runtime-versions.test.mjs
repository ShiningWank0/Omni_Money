import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { checkRuntimeVersions, syncRuntimeVersions } from './runtime-versions.mjs'

function fixture(t, { go = '1.26.7', node = '24.19.0', digest = 'a'.repeat(64) } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'omni-runtime-versions-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeFileSync(join(root, 'go.mod'), 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.7\n')
  writeFileSync(join(root, '.node-version'), '24.19.0\n')
  writeFileSync(join(root, '.go-version'), '1.26.7\n')
  writeFileSync(join(root, 'Dockerfile'),
    `FROM node:${node}-alpine@sha256:${digest} AS frontend-builder\nFROM golang:${go}-alpine@sha256:${digest} AS backend-builder\n`)
  return root
}

test('aligned pins pass without rewriting files', t => {
  const root = fixture(t)
  const before = readFileSync(join(root, 'go.mod'), 'utf8')
  assert.equal(checkRuntimeVersions(root).toolchain, '1.26.7')
  syncRuntimeVersions(root)
  assert.equal(readFileSync(join(root, 'go.mod'), 'utf8'), before)
})

test('a Docker-only update fails until both pins are synchronized', t => {
  const root = fixture(t, { go: '1.27.1', node: '26.10.0' })
  assert.throws(() => checkRuntimeVersions(root), /Go toolchain.*Node version file.*mismatch/)
  syncRuntimeVersions(root)
  assert.equal(checkRuntimeVersions(root).toolchain, '1.27.1')
  assert.equal(readFileSync(join(root, '.node-version'), 'utf8'), '26.10.0\n')
  assert.equal(readFileSync(join(root, '.go-version'), 'utf8'), '1.27.1\n')
  assert.match(readFileSync(join(root, 'go.mod'), 'utf8'), /^go 1\.26\.6$/m)
})

test('rejects downgrades below minimum Go without modifying either file', t => {
  const root = fixture(t, { go: '1.26.5', node: '26.10.0' })
  assert.throws(() => syncRuntimeVersions(root), /older than go.mod minimum/)
  assert.match(readFileSync(join(root, 'go.mod'), 'utf8'), /toolchain go1\.26\.7/)
  assert.equal(readFileSync(join(root, '.node-version'), 'utf8'), '24.19.0\n')
})

test('rejects missing digests and ambiguous builder stages', t => {
  const root = fixture(t, { digest: 'not-a-digest' })
  assert.throws(() => syncRuntimeVersions(root), /digest-pinned/)
  const valid = fixture(t)
  const path = join(valid, 'Dockerfile')
  writeFileSync(path, readFileSync(path, 'utf8').repeat(2))
  assert.throws(() => syncRuntimeVersions(valid), /exactly one/)
})

test('Wails CLI metadata rejects stale modules, replacements and toolchains', async () => {
  const { verifyWailsCLI } = await import('./install-wails-v3.mjs')
  const info = 'wails3: go1.27.4\n mod github.com/wailsapp/wails/v3 v3.1.0 h1:wails\n dep golang.org/x/tools v0.52.0 h1:tools\n dep golang.org/x/net v0.62.0 h1:net\n'
  verifyWailsCLI(info, 'v3.1.0', '1.27.4')
  verifyWailsCLI(info.replaceAll('\n', '\r\n'), 'v3.1.0', '1.27.4')
  for (const changed of [info.replace('go1.27.4', 'go1.26.11'), info.replace('v3.1.0', 'v3.0.0'),
    info.replace('v0.52.0', 'v0.49.0'), info.replace('v0.62.0', 'v0.61.0'),
    info + ' => ../replaced\n', info + ' dep golang.org/x/net v0.62.0 h1:duplicate\n',
    info.split('\n').filter(line => !line.includes('x/tools')).join('\n')]) {
    assert.throws(() => verifyWailsCLI(changed, 'v3.1.0', '1.27.4'))
  }
})
