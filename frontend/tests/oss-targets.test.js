import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { resolveDisplayTarget, selectDisplayCatalog, verifyDisplayInventory } from '../build/oss-catalog.mjs'

const catalog = JSON.parse(readFileSync(new URL('../src/generated/thirdPartyNotices.json', import.meta.url), 'utf8'))
const targets = JSON.parse(readFileSync(new URL('../../licenses/targets.json', import.meta.url), 'utf8'))
const stable = value => `${JSON.stringify(value, null, 2)}\n`
const hash = value => createHash('sha256').update(value).digest('hex')

for (const target of targets) test(`shows exactly the reviewed dependencies for ${target.id}`, () => {
  const selected = selectDisplayCatalog(catalog, target.id)
  assert.deepEqual(selected.target, { id: target.id, mode: target.mode, goos: target.goos, goarch: target.goarch })
  assert.deepEqual(selected.components.map(component => component.id), catalog.components.filter(component => component.targets.includes(target.id)).map(component => component.id))
  assert.ok(selected.components.every(component => component.targets.length === 1 && component.targets[0] === target.id))
  assert.equal(selected.components.some(component => component.id === 'go:github.com/wailsapp/wails/v2'), target.mode === 'desktop')
  assert.equal(selected.components.some(component => component.id === 'go:github.com/go-webauthn/webauthn'), target.mode === 'server')
  assert.equal(selected.components.some(component => component.id === 'go:github.com/wailsapp/go-webview2'), target.goos === 'windows')
  const openssl = selected.components.find(component => component.id === 'native:openssl')
  assert.equal(openssl.version.includes('macOS:'), false)
  assert.equal(openssl.version, target.goos === 'darwin' ? catalog.components.find(component => component.id === 'native:openssl').version.match(/macOS: ([\d.]+)/)[1] : '3.x')
})

test('CPU-specific components and versions cannot leak between builds of the same mode', () => {
  const fixture = { ...catalog, components: [
    { id: 'npm:common', name: 'common', version: '1.0.0', targets: ['server-linux-amd64', 'server-linux-arm64'], files: [] },
    { id: 'go:amd64-only', name: 'amd64-only', version: '2.0.0', targets: ['server-linux-amd64'], files: [] },
    { id: 'go:arm64-only', name: 'arm64-only', version: '3.0.0', targets: ['server-linux-arm64'], files: [] },
  ] }
  assert.deepEqual(selectDisplayCatalog(fixture, 'server-linux-amd64').components.map(component => component.id), ['npm:common', 'go:amd64-only'])
  assert.deepEqual(selectDisplayCatalog(fixture, 'server-linux-arm64').components.map(component => component.id), ['npm:common', 'go:arm64-only'])
})

test('build selection uses the distribution target rather than the viewing browser platform', () => {
  assert.equal(resolveDisplayTarget({ VITE_OSS_TARGET: 'server-linux-arm64' }, { platform: 'win32', arch: 'x64' }), 'server-linux-arm64')
  assert.equal(resolveDisplayTarget({ OMNI_OSS_MODE: 'desktop' }, { platform: 'win32', arch: 'x64' }), 'desktop-windows-amd64')
  assert.equal(resolveDisplayTarget({ OMNI_OSS_MODE: 'desktop' }, { platform: 'darwin', arch: 'arm64' }), 'desktop-darwin-arm64')
  assert.equal(resolveDisplayTarget({ BUILD_PLATFORM: 'darwin/amd64' }, { platform: 'darwin', arch: 'arm64' }), 'desktop-darwin-amd64')
  assert.throws(() => resolveDisplayTarget({ VITE_OSS_TARGET: 'all' }), /Unknown/)
  assert.throws(() => resolveDisplayTarget({ OMNI_OSS_MODE: 'desktop', VITE_OSS_TARGET: 'server-linux-amd64' }), /inconsistent/)
  assert.throws(() => resolveDisplayTarget({ BUILD_PLATFORM: 'darwin/arm64', VITE_OSS_TARGET: 'desktop-darwin-amd64' }), /inconsistent/)
  assert.throws(() => selectDisplayCatalog(catalog, 'desktop-linux-arm64'), /Unknown/)
})

test('packaging rejects another target, stale display metadata and changed JavaScript', () => {
  const root = mkdtempSync(join(tmpdir(), 'omni-oss-display-'))
  const target = 'server-linux-arm64'
  const selected = selectDisplayCatalog(catalog, target)
  const code = `export default ${JSON.stringify(selected)}`
  const inventory = { schema: 1, target, catalogSHA256: hash(stable(selected)),
    components: selected.components.map(({ id, version }) => ({ id, version })), chunks: [{ name: 'assets/LicenseDialog-test.js', sha256: hash(code) }] }
  try {
    mkdirSync(join(root, 'dist/assets'), { recursive: true })
    writeFileSync(join(root, 'dist/assets/LicenseDialog-test.js'), code)
    writeFileSync(join(root, 'oss-display-inventory.json'), stable(inventory))
    assert.doesNotThrow(() => verifyDisplayInventory(root, catalog, target))
    assert.throws(() => verifyDisplayInventory(root, catalog, 'server-linux-amd64'), /differs/)
    const changed = structuredClone(catalog)
    changed.components.find(component => component.id === 'native:sqlcipher').version = 'changed'
    assert.throws(() => verifyDisplayInventory(root, changed, target), /differs/)
    writeFileSync(join(root, 'dist/assets/LicenseDialog-test.js'), 'changed output')
    assert.throws(() => verifyDisplayInventory(root, catalog, target), /missing or changed/)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
