import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { checkRuntimeVersions, syncRuntimeVersions } from './runtime-versions.mjs'

function fixture(t, { go = '1.26.7', node = '24.19.0', digest = 'a'.repeat(64) } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'omni-runtime-versions-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeFileSync(join(root, 'go.mod'), 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.7\n')
  writeFileSync(join(root, '.node-version'), '24.19.0\n')
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
