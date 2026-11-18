import test from 'node:test'
import assert from 'node:assert/strict'
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { auditPatchedPicomatch, classifyUpstreamAdvisories, projectRoot, verifyPicomatchPatch } from './verify-picomatch-patch.mjs'

const manifest = JSON.parse(readFileSync(join(projectRoot, 'scripts/security-patches/picomatch.json'), 'utf8'))
const record = JSON.parse(readFileSync(join(projectRoot, 'scripts/security-patches/picomatch-advisory.json'), 'utf8'))
const identity = manifest.advisoryIdentity

test('new and revised upstream advisories are not waived by the local patch', () => {
  assert.equal(classifyUpstreamAdvisories({ picomatch: [identity] }, manifest).remediations.length, 1)
  const fresh = { ...identity, id: 9999999, url: 'https://github.com/advisories/GHSA-aaaa-bbbb-cccc' }
  const result = classifyUpstreamAdvisories({ picomatch: [identity, fresh] }, manifest)
  assert.equal(result.findings.length, 1); assert.equal(result.remediations.length, 1)
  assert.equal(classifyUpstreamAdvisories({ picomatch: [{ ...identity, title: 'new bypass of the same CVE' }] }, manifest).findings.length, 1)
  assert.throws(() => classifyUpstreamAdvisories({ error: 'not an audit' }, manifest))
})

test('network failure, changed full advisory content and malformed data fail the audit', async () => {
  const fetcher = data => async url => url.includes('osv.dev')
    ? { ok: true, text: async () => JSON.stringify(data) }
    : { ok: true, text: async () => JSON.stringify({ picomatch: [identity] }) }
  assert.equal((await auditPatchedPicomatch(projectRoot, fetcher(record))).remediations.length, 1)
  await assert.rejects(auditPatchedPicomatch(projectRoot, fetcher({ ...record, modified: '2099-01-01T00:00:00Z' })), /advisory changed/)
  await assert.rejects(auditPatchedPicomatch(projectRoot, async () => ({ ok: false })), /check failed/)
  await assert.rejects(auditPatchedPicomatch(projectRoot, async () => ({ ok: true, text: async () => '{}' })), /recorded advisory|undefined|equal/i)
})

test('source drift and nested unpatched packages fail before an advisory can be treated as remediated', t => {
  const root = mkdtempSync(join(tmpdir(), 'omni-picomatch-integrity-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const path of ['frontend/vendor/picomatch', 'frontend/package.json', 'frontend/package-lock.json', 'scripts/security-patches']) {
    mkdirSync(join(root, path, '..'), { recursive: true })
    cpSync(join(projectRoot, path), join(root, path), { recursive: true })
  }
  mkdirSync(join(root, 'frontend/node_modules'))
  symlinkSync(join(root, 'frontend/vendor/picomatch'), join(root, 'frontend/node_modules/picomatch'), 'junction')
  verifyPicomatchPatch(root)
  const source = join(root, 'frontend/vendor/picomatch/lib/parse.js'), original = readFileSync(source)
  writeFileSync(source, Buffer.concat([original, Buffer.from('\n// changed\n')]))
  assert.throws(() => verifyPicomatchPatch(root), /source changed/)
  writeFileSync(source, original)
  const path = join(root, 'frontend/package-lock.json'), lock = JSON.parse(readFileSync(path, 'utf8'))
  lock.packages['node_modules/unrelated/node_modules/picomatch'] = { version: '4.0.7' }
  writeFileSync(path, JSON.stringify(lock))
  assert.throws(() => verifyPicomatchPatch(root), /Unpatched nested/)
})
