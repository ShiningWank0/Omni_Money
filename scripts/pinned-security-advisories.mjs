import assert from 'node:assert/strict'
import { compareVersion } from './pinned-security-inventory.mjs'

const normalize = value => {
  assert.match(value, /^\d+(?:\.\d+){0,3}$/)
  return value.includes('.') ? value : value + '.0'
}

export function classifyCNA(record, source) {
  assert.equal(record.dataType, 'CVE_RECORD'); assert.ok(record.cveMetadata?.cveId)
  if (record.cveMetadata.state === 'REJECTED') return 'unaffected'
  assert.ok(Array.isArray(record.containers?.cna?.affected), 'CNA affected-version data missing')
  const affected = record.containers.cna.affected.filter(x =>
    String(x.product || x.packageName || '').toLowerCase().includes(source.product))
  // A keyword mention is not sufficient to assert that this product/version
  // is affected, but is retained as an advisory needing human review.
  if (!affected.length) return 'review'
  let uncertain = false
  for (const product of affected) {
    let matched = false
    for (const version of product.versions || []) {
      try {
        assert.ok(!version.changes?.length, 'version transitions need review')
        assert.ok(!version.versionType || ['semver', 'custom'].includes(version.versionType))
        let applies
        if (version.version === '*') applies = true
        else {
          const start = normalize(version.version), installed = normalize(source.version)
          if (version.lessThan || version.lessThanOrEqual) {
            applies = compareVersion(installed, start) >= 0
            if (version.lessThan && version.lessThan !== '*') applies &&= compareVersion(installed, normalize(version.lessThan)) < 0
            if (version.lessThanOrEqual && version.lessThanOrEqual !== '*') applies &&= compareVersion(installed, normalize(version.lessThanOrEqual)) <= 0
          } else applies = compareVersion(installed, start) === 0
        }
        if (!applies) continue
        matched = true
        if (version.status === 'affected') return 'affected'
        if (version.status !== 'unaffected') uncertain = true
      } catch { uncertain = true }
    }
    if (!matched && product.defaultStatus !== 'unaffected') uncertain = true
  }
  return uncertain ? 'review' : 'unaffected'
}

export function nvdPage(data, start) {
  assert.ok(Number.isInteger(data.totalResults) && data.totalResults >= 0 && data.totalResults <= 10000)
  assert.equal(data.startIndex, start); assert.ok(Array.isArray(data.vulnerabilities))
  assert.ok(data.totalResults === 0 || data.vulnerabilities.length > 0, 'NVD pagination stopped early')
  const ids = data.vulnerabilities.map(x => x.cve?.id)
  assert.ok(ids.every(x => /^CVE-\d{4}-\d+$/.test(x)), 'NVD advisory identity missing')
  return ids
}
