import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, readdirSync, realpathSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export const advisoryID = 'GHSA-fc5q-39xc-vjpf'
export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
export const canonical = value => JSON.stringify(value, function (_, item) {
  return item && typeof item === 'object' && !Array.isArray(item)
    ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item
})
const json = path => JSON.parse(readFileSync(path, 'utf8'))

export function verifyPicomatchPatch(root = projectRoot) {
  const manifest = json(join(root, 'scripts/security-patches/picomatch.json'))
  assert.equal(manifest.schema, 1)
  assert.equal(manifest.advisory, advisoryID)
  assert.equal(manifest.version, '4.0.7')
  const frontend = join(root, 'frontend'), vendor = join(frontend, 'vendor/picomatch')
  const paths = base => readdirSync(base, { withFileTypes: true }).flatMap(entry => {
    assert.ok(!entry.isSymbolicLink(), 'Unexpected symlink in patched source')
    assert.ok(entry.isDirectory() || entry.isFile(), 'Unexpected special file in patched source')
    const path = join(base, entry.name)
    return entry.isDirectory() ? paths(path) : [path.slice(vendor.length + 1).replaceAll('\\', '/')]
  })
  assert.deepEqual(paths(vendor).sort(), Object.keys(manifest.files).sort(), 'Patched source file set changed')
  for (const [path, expected] of Object.entries(manifest.files)) {
    assert.match(expected, /^[a-f0-9]{64}$/)
    assert.equal(sha256(readFileSync(join(vendor, path))), expected, 'Patched source changed: ' + path)
  }
  const pkg = json(join(vendor, 'package.json'))
  assert.equal(pkg.name, 'picomatch'); assert.equal(pkg.version, manifest.version)
  assert.equal(realpathSync(join(frontend, 'node_modules/picomatch')), realpathSync(vendor), 'Installed picomatch is not the verified local patch')
  const lock = json(join(frontend, 'package-lock.json')), definition = json(join(frontend, 'package.json'))
  assert.equal(definition.devDependencies.picomatch, 'file:vendor/picomatch')
  assert.equal(definition.overrides.picomatch, '$picomatch')
  assert.equal(lock.packages['vendor/picomatch'].version, manifest.version)
  for (const [path, item] of Object.entries(lock.packages)) {
    if (path.endsWith('node_modules/picomatch')) {
      assert.equal(item.link, true, 'Unpatched nested picomatch in lock')
      assert.equal(resolve(frontend, item.resolved), vendor)
    }
    if (!path || ![item.dependencies, item.optionalDependencies, item.peerDependencies].some(x => x?.picomatch)) continue
    const importer = createRequire(join(frontend, path, 'package.json'))
    assert.equal(realpathSync(importer.resolve('picomatch')), join(realpathSync(vendor), 'index.js'), 'An importer resolves unpatched picomatch: ' + path)
  }
  return manifest
}

export function classifyUpstreamAdvisories(data, manifest) {
  assert.ok(data && typeof data === 'object' && !Array.isArray(data))
  assert.ok(Object.keys(data).every(name => name === 'picomatch'), 'Unexpected upstream audit package')
  assert.ok(data.picomatch === undefined || Array.isArray(data.picomatch))
  const findings = [], remediations = [], candidates = []
  for (const item of data.picomatch || []) {
    assert.ok(Number.isSafeInteger(item.id) && item.id > 0)
    assert.match(item.url, /^https:\/\/github\.com\/advisories\/GHSA-[a-z0-9-]+$/)
    assert.ok(['info', 'low', 'moderate', 'high', 'critical'].includes(item.severity))
    assert.ok(typeof item.title === 'string' && typeof item.vulnerable_versions === 'string')
    const identity = { id: item.id, url: item.url, title: item.title, severity: item.severity, vulnerable_versions: item.vulnerable_versions }
    const id = item.url.split('/').at(-1)
    if (id === advisoryID && sha256(JSON.stringify(identity)) === manifest.advisoryIdentitySHA256) {
      remediations.push({ id, component: 'picomatch', installed: manifest.version, patchRevision: manifest.patchRevision,
        sourceSHA256: manifest.files['lib/parse.js'], trackingIssue: manifest.trackingIssue })
    } else {
      const finding = { tool: 'npm', id, component: 'picomatch', installed: manifest.version, fixed: '',
        severity: item.severity.toUpperCase(), summary: item.title, context: 'upstream advisory check for verified local dependency' }
      candidates.push(finding)
      if (['high', 'critical'].includes(item.severity)) findings.push(finding)
    }
  }
  return { findings, remediations, candidates }
}

export async function auditPatchedPicomatch(root = projectRoot, fetcher = fetch) {
  const manifest = verifyPicomatchPatch(root)
  const response = await fetcher('https://registry.npmjs.org/-/npm/v1/security/advisories/bulk', {
    method: 'POST', redirect: 'error', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ picomatch: [manifest.version] }), signal: AbortSignal.timeout(30_000),
  })
  assert.ok(response.ok, 'Upstream npm advisory check failed')
  const raw = await response.text()
  assert.ok(raw.length < 1024 * 1024, 'Oversized upstream advisory response')
  const data = JSON.parse(raw)
  const advisory = await fetcher(`https://api.osv.dev/v1/vulns/${advisoryID}`, { redirect: 'error', signal: AbortSignal.timeout(30_000) })
  assert.ok(advisory.ok, 'Recorded advisory cannot be revalidated')
  const record = JSON.parse(await advisory.text())
  assert.equal(record.id, advisoryID)
  assert.equal(sha256(canonical(record)), manifest.advisoryRecordSHA256, 'The recorded advisory changed; review the local fix again')
  return { ...classifyUpstreamAdvisories(data, manifest), upstream: data }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { const manifest = verifyPicomatchPatch(); console.log(`Verified picomatch ${manifest.version}, local patch ${manifest.patchRevision}, tracking #${manifest.trackingIssue}`) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
