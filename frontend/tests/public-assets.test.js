import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { bundleManifest, verifyBuildTree } from '../build/public-assets.mjs'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'omni-public-assets-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const bundle = {
    html: { type: 'asset', fileName: 'index.html', source: '<html></html>' },
    js: { type: 'chunk', fileName: 'assets/app.js', code: 'public' },
    icon: { type: 'asset', fileName: 'assets/icon.svg', source: '<svg/>' },
  }
  for (const output of Object.values(bundle)) {
    const filename = join(root, output.fileName)
    mkdirSync(dirname(filename), { recursive: true })
    writeFileSync(filename, output.code ?? output.source)
  }
  return { root, bundle, manifest: bundleManifest(bundle) }
}

test('the public inventory covers only bundler outputs and verifies their actual bytes', t => {
  const { root, bundle, manifest } = fixture(t)
  assert.deepEqual(manifest.files.map(file => file.name), ['assets/app.js', 'assets/icon.svg', 'index.html'])
  assert.doesNotThrow(() => verifyBuildTree(root, manifest))
  writeFileSync(join(root, 'assets/app.js'), 'private-marker')
  assert.throws(() => verifyBuildTree(root, manifest), /hash mismatch/)
  assert.deepEqual(bundleManifest(bundle), manifest)
})

for (const name of ['.env', 'assets/control.db', 'assets/control.key', 'assets/app.js.map', 'data.tar', 'assets/unexpected.svg']) {
  test(`an extra ${name} is rejected even when its extension could be served`, t => {
    const { root, manifest } = fixture(t)
    writeFileSync(join(root, name), 'private-marker')
    assert.throws(() => verifyBuildTree(root, manifest), /Unexpected public output/)
  })
}

test('links to files and directories are rejected in the build tree', t => {
  const { root, manifest } = fixture(t)
  rmSync(join(root, 'assets/icon.svg'))
  symlinkSync('app.js', join(root, 'assets/icon.svg'))
  assert.throws(() => verifyBuildTree(root, manifest), /Linked public output/)
  rmSync(join(root, 'assets/icon.svg'))
  writeFileSync(join(root, 'assets/icon.svg'), '<svg/>')
  const alias = join(root, 'alias')
  symlinkSync('assets', alias, 'dir')
  assert.throws(() => verifyBuildTree(root, manifest), /Linked public output/)
})

test('forbidden filenames cannot be added to the bundler inventory', t => {
  const { bundle } = fixture(t)
  for (const name of ['.env', 'assets/.private.svg', 'assets/control.db', 'assets/key.pem', '../escape.js', 'assets/app.js.map', 'assets/file.svg:stream']) {
    assert.throws(() => bundleManifest({ ...bundle, extra: { type: 'asset', fileName: name, source: 'private-marker' } }), /Forbidden/)
  }
})

test('recognized private bytes fail even if deliberately emitted as an allowed asset', t => {
  const { root, bundle } = fixture(t)
  for (const source of ['-----BEGIN PRIVATE KEY-----\nprivate-marker', 'SQLite format 3\0private-marker']) {
    writeFileSync(join(root, 'assets/icon.svg'), source)
    const manifest = bundleManifest({ ...bundle, icon: { ...bundle.icon, source } })
    assert.throws(() => verifyBuildTree(root, manifest), error => /Private material/.test(error.message) && !error.message.includes('private-marker'))
  }
})
