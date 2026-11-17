import assert from 'node:assert/strict'
import test from 'node:test'
import { msysSBOM, recipeIndex, upstreamVersion } from './pinned-security-msys.mjs'

const text = 'pkgbase = mingw-w64-example\npkgver = 2.0.0\npkgrel = 3\npkgname = mingw-w64-ucrt-x86_64-example\npkgname = mingw-w64-ucrt-x86_64-example-libs\n'
const cache = { example: { srcinfo: { ucrt64: text } } }
const component = { type: 'library', name: 'example', version: '2.0.0', cpe: 'cpe:/a:vendor:example:2.0.0',
  properties: [{ name: 'msys2:pkgbase', value: 'mingw-w64-example' }] }
const sbom = { bomFormat: 'CycloneDX', components: [component] }
const packageName = 'mingw-w64-ucrt-x86_64-example'

test('SRCINFO split-package names map to their actual recipe without executing PKGBUILD', () => {
  const index = recipeIndex(cache)
  assert.equal(index.get(packageName).base, 'mingw-w64-example')
  assert.equal(index.get(packageName + '-libs').fullVersion, '2.0.0-3')
  assert.equal(upstreamVersion('1:2.0.0+git-3'), '2.0.0')
  assert.throws(() => recipeIndex({}), /empty/)
})

test('old snapshots use their saved version, never the latest recipe version or latest VEX', () => {
  const value = msysSBOM(`${packageName} 1.0.0-2\n`, [{ cache, sbom }])
  assert.equal(value.sbom.components[0].version, '1.0.0')
  assert.equal(value.sbom.components[0].cpe, 'cpe:/a:vendor:example:1.0.0')
  assert.equal(value.identities.get('msys2/' + packageName).installed, '1.0.0-2')
  assert.equal(value.mappedCount, 1); assert.deepEqual(value.gaps, [])
  assert.equal(value.sbom.vulnerabilities, undefined)
})

test('formatted CPE and PURL versions are replaced while preserving product identity', () => {
  const components = [
    { ...component, cpe: 'cpe:2.3:a:vendor:example:2.0.0:*:*:*:*:*:*:*' },
    { ...component, cpe: undefined, purl: 'pkg:generic/example@2.0.0?arch=x86_64' },
  ]
  const value = msysSBOM(`${packageName} 1.0.0-2`, [{ cache, sbom: { ...sbom, components } }])
  assert.equal(value.sbom.components[0].cpe, 'cpe:2.3:a:vendor:example:1.0.0:*:*:*:*:*:*:*')
  assert.equal(value.sbom.components[1].purl, 'pkg:generic/example@1.0.0?arch=x86_64')
})

test('ecosystem version overrides cannot be guessed for an older snapshot', () => {
  const metadata = { ...sbom, components: [{ ...component, version: '2.0.post1' }] }
  const older = msysSBOM(`${packageName} 1.0.0-2`, [{ cache, sbom: metadata }])
  assert.equal(older.sbom.components.length, 0); assert.equal(older.gaps.length, 1)
  const exact = msysSBOM(`${packageName} 2.0.0-3`, [{ cache, sbom: metadata }])
  assert.equal(exact.sbom.components[0].version, '2.0.post1')
})

test('missing mappings, metadata-free packages, malformed inventories and contradictory recipes never become clean', () => {
  const unmapped = msysSBOM(`${packageName} 1.0.0-2\nunknown 1.0-1`, [{ cache, sbom: { ...sbom, components: [{ ...component, cpe: undefined }] } }])
  assert.equal(unmapped.gaps.length, 2); assert.equal(unmapped.mappedCount, 0)
  for (const packages of ['', 'bad line extra', `${packageName} 1.0-1\n${packageName} 1.0-1`]) assert.throws(() => msysSBOM(packages, [{ cache, sbom }]))
  assert.throws(() => recipeIndex({ one: { srcinfo: { a: text, b: text.replace('2.0.0', '3.0.0') } } }), /ambiguous/)
})
