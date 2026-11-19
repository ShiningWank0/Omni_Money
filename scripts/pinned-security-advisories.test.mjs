import assert from 'node:assert/strict'
import test from 'node:test'
import { classifyCNA, nvdPage } from './pinned-security-advisories.mjs'

const source = { product: 'sqlcipher', version: '4.18.0' }
const record = versions => ({ dataType: 'CVE_RECORD', cveMetadata: { cveId: 'CVE-2099-1234', state: 'PUBLISHED' },
  containers: { cna: { affected: [{ product: 'SQLCipher', defaultStatus: 'unaffected', versions }] } } })

test('CNA ranges respect inclusive/exclusive bounds and exact installed versions', () => {
  assert.equal(classifyCNA(record([{ version: '0', lessThan: '4.19.0', versionType: 'semver', status: 'affected' }]), source), 'affected')
  assert.equal(classifyCNA(record([{ version: '0', lessThan: '4.18.0', status: 'affected' }]), source), 'unaffected')
  assert.equal(classifyCNA(record([{ version: '0', lessThanOrEqual: '4.18.0', status: 'affected' }]), source), 'affected')
  assert.equal(classifyCNA(record([{ version: '4.18.0', status: 'affected' }]), source), 'affected')
})

test('unsupported ranges and keyword-only matches require review rather than asserting vulnerability or safety', () => {
  assert.equal(classifyCNA(record([{ version: 'before 5', status: 'affected' }]), source), 'review')
  assert.equal(classifyCNA(record([{ version: '0', lessThan: '5.0', versionType: 'git', status: 'affected' }]), source), 'review')
  const unrelated = record([]); unrelated.containers.cna.affected[0].product = 'Unrelated app using SQL library'
  assert.equal(classifyCNA(unrelated, source), 'review')
  const rejected = record([]); rejected.cveMetadata.state = 'REJECTED'
  assert.equal(classifyCNA(rejected, source), 'unaffected')
})

test('NVD pagination requires valid identities and cannot treat a truncated page as a complete scan', () => {
  assert.deepEqual(nvdPage({ totalResults: 1, startIndex: 0, vulnerabilities: [{ cve: { id: 'CVE-2099-1234' } }] }, 0), ['CVE-2099-1234'])
  for (const value of [{ totalResults: 1, startIndex: 0, vulnerabilities: [] }, { totalResults: 1, startIndex: 1, vulnerabilities: [] },
    { totalResults: 1, startIndex: 0, vulnerabilities: [{ cve: { id: 'invalid' } }] }]) assert.throws(() => nvdPage(value, 0))
})
