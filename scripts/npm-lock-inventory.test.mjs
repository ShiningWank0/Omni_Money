import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { npmPackages } from '../frontend/build/npm-lock.mjs'
import { bundledPackages } from '../frontend/build/oss-inventory.mjs'
import { usageFromInputs } from './oss-notices.mjs'

const lock = () => ({ lockfileVersion: 3, packages: { '': {},
  'node_modules/picomatch': { resolved: 'vendor/picomatch', link: true },
  'vendor/picomatch': { version: '4.0.7', dev: true, license: 'MIT' },
  'node_modules/regular': { version: '1.2.3', dev: true, license: 'MIT' },
} })

test('npm links inherit real metadata without counting source records as packages', () => {
  const packages = npmPackages(lock())
  assert.equal(packages.length, 2)
  const local = packages.find(pkg => pkg.name === 'picomatch')
  assert.equal(local.version, '4.0.7'); assert.equal(local.dev, true)
  assert.equal(local.sourceDirectory, 'vendor/picomatch')
  const usage = usageFromInputs({ components: [] }, lock(), []).dependencies
  assert.equal(usage.length, 2)
  assert.deepEqual(usage.find(pkg => pkg.id === 'npm:picomatch'), {
    id: 'npm:picomatch', name: 'picomatch', version: '4.0.7', dependencyType: 'development', distribution: 'excluded',
    targets: [], includedTargets: [], externalTargets: [], license: null,
    evidence: 'Not present in production frontend bundle module inventory', sourceDirectory: 'vendor/picomatch',
  })
  assert.ok(usage.every(pkg => pkg.version && pkg.name !== 'atch'))
})

test('missing, cyclic, escaping and contradictory link metadata never yields a partial inventory', () => {
  for (const mutate of [
    value => { delete value.packages['vendor/picomatch'] },
    value => { value.packages['vendor/picomatch'] = { link: true, resolved: 'node_modules/picomatch' } },
    value => { value.packages['node_modules/picomatch'].resolved = '../outside' },
    value => { value.packages['node_modules/picomatch'].version = '0.0.0' },
    value => { delete value.packages['vendor/picomatch'].version },
  ]) { const value = lock(); mutate(value); assert.throws(() => npmPackages(value)) }
})

test('a linked package retained by the bundle is included even when it is a dev dependency', t => {
  const root = mkdtempSync(join(tmpdir(), 'omni-local-bundle-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  mkdirSync(join(root, 'vendor/picomatch/lib'), { recursive: true })
  writeFileSync(join(root, 'package-lock.json'), JSON.stringify(lock()))
  writeFileSync(join(root, 'vendor/picomatch/package.json'), JSON.stringify({ name: 'picomatch', version: '4.0.7' }))
  const inventory = bundledPackages({ app: { type: 'chunk', modules: { [join(root, 'vendor/picomatch/lib/utils.js')]: {} } } }, root)
  assert.deepEqual(inventory, [{ name: 'picomatch', version: '4.0.7', directory: 'node_modules/picomatch' }])
  const usage = usageFromInputs({ components: [{ id: 'npm:picomatch', version: '4.0.7', targets: ['server-linux-amd64'], license: 'MIT' }] }, lock(), []).dependencies
  assert.equal(usage.find(pkg => pkg.id === 'npm:picomatch').distribution, 'included')
})

test('the narrow Docker context includes the complete frontend build-helper import graph and patched source', () => {
  const root = fileURLToPath(new URL('../', import.meta.url))
  const allow = new Set(readFileSync(join(root, '.dockerignore'), 'utf8').split('\n').filter(line => line.startsWith('!')).map(line => line.slice(1)))
  const seen = new Set()
  const visit = path => {
    if (seen.has(path)) return
    seen.add(path)
    const local = relative(root, path).replaceAll('\\', '/')
    assert.ok(!local.startsWith('../') && allow.has(local), `Build helper is missing from Docker context: ${local}`)
    const source = readFileSync(path, 'utf8')
    for (const [, imported] of source.matchAll(/\bfrom\s+['"](\.[^'"]+)['"]/g)) visit(resolve(dirname(path), imported))
  }
  for (const path of ['frontend/vite.config.js', 'scripts/oss-notices.mjs', 'scripts/verify-picomatch-patch.mjs']) visit(join(root, path))
  const manifest = JSON.parse(readFileSync(join(root, 'scripts/security-patches/picomatch.json'), 'utf8'))
  for (const path of Object.keys(manifest.files)) assert.ok(allow.has('frontend/vendor/picomatch/' + path), `Verified source missing from Docker context: ${path}`)
})

test('the committed npm usage inventory matches the actual lock before any Go metadata is fetched', () => {
  const root = fileURLToPath(new URL('../', import.meta.url))
  const read = path => JSON.parse(readFileSync(join(root, path), 'utf8'))
  const expected = usageFromInputs(read('frontend/src/generated/thirdPartyNotices.json'), read('frontend/package-lock.json'), []).dependencies.filter(entry => entry.id.startsWith('npm:'))
  const actual = read('licenses/dependency-usage.json').dependencies.filter(entry => entry.id.startsWith('npm:'))
  assert.deepEqual(actual, expected)
})
