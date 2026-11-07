import assert from 'node:assert/strict'
import { readFileSync, mkdirSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import test from 'node:test'
import { noticeText, parseObjects, targets, targetConfigs, validateCatalog, verifyBundledPackages, validateTargetCoverage, root } from './oss-notices.mjs'
import { verifyDependencies, verifyNativeLicense } from './oss-artifact.mjs'
import { bundledPackages } from '../frontend/build/oss-inventory.mjs'

const catalog = JSON.parse(readFileSync(new URL('../frontend/src/generated/thirdPartyNotices.json', import.meta.url)))
const approved = JSON.parse(readFileSync(new URL('../licenses/approved.json', import.meta.url)))
test('reviewed source notices include copyright, nested license boundaries and no test tools', () => {
  validateCatalog(catalog, approved)
  validateTargetCoverage()
  const webview = catalog.components.find(component => component.id === 'go:github.com/wailsapp/go-webview2')
  assert.ok(webview.files.some(file => file.path === 'webviewloader/LICENSE'))
  assert.ok(catalog.components.every(component => !/vitest|playwright|jsdom/.test(component.id)))
  assert.ok(catalog.components.find(component => component.id === 'npm:vite'), 'Vite preload helper code is shipped')
})
for (const [name, mutate, pattern] of [
  ['missing component approval', (_catalog, approval) => { delete approval.components['npm:pinia'] }, /review required/],
  ['unreviewed dependency version', catalog => { catalog.components[0].version = 'unreviewed' }, /review required/],
  ['missing license text', catalog => { catalog.components[0].files = [] }, /Missing license text/],
  ['missing application identity', catalog => { delete catalog.applicationModule }, /application module/],
  ['changed copyright or NOTICE', catalog => { catalog.components[0].files[0].text += 'altered' }, /NOTICE changed/],
  ['unknown license', catalog => { catalog.components[0].license = 'unreviewed' }, /Unreviewed license/],
  ['duplicate components', catalog => { catalog.components.push(catalog.components[0]) }, /Duplicate/],
  ['removed dependency with stale approval', catalog => { catalog.components.pop() }, /Stale OSS approvals/],
]) test(`fails closed on ${name}`, () => {
  const candidate = structuredClone(catalog), approval = structuredClone(approved)
  mutate(candidate, approval)
  assert.throws(() => validateCatalog(candidate, approval), pattern)
})
test('each release target gets the notices for its shipped dependencies', () => {
  for (const target of targets) {
    const text = noticeText(catalog, target)
    assert.match(text, /ZETETIC/)
    assert.equal(text.includes('github.com/wailsapp/wails/v2'), target.startsWith('desktop-'))
    assert.equal(text.includes('github.com/go-webauthn/webauthn'), target.startsWith('server-'))
  }
  assert.throws(() => noticeText(catalog, 'unknown'), /Unknown OSS target/)
})
test('Go inventory parser handles braces and escaped quotes inside JSON strings', () => {
  assert.deepEqual(parseObjects('{"value":"a } { \\\" quote"}\n{"value":2}\n'), [{ value: 'a } { " quote' }, { value: 2 }])
  assert.throws(() => parseObjects('{"missing":'), /Incomplete/)
})
test('packaged Go build information must match the reviewed target and toolchain', () => {
  const target = 'desktop-windows-amd64'
  const expected = catalog.components.filter(component => component.id.startsWith('go:') && component.targets.includes(target))
  const goVersion = catalog.components.find(component => component.id === 'native:go').version
  const config = targetConfigs.find(config => config.id === target)
  const info = `omni_money.exe: go${goVersion}\n\tbuild\tGOOS=${config.goos}\n\tbuild\tGOARCH=${config.goarch}\n` + expected.map(component => `\tdep\t${component.name}\t${component.version}\th1:checksum\n`).join('')
  assert.doesNotThrow(() => verifyDependencies(info, catalog, target))
  assert.doesNotThrow(() => verifyDependencies(info + `\tdep\t${catalog.applicationModule}\t(devel)\n`, catalog, target))
  assert.throws(() => verifyDependencies(info + `\tdep\t${catalog.applicationModule}\tv1.0.0\n`, catalog, target), /differ/)
  assert.throws(() => verifyDependencies(info + '\tdep\tother.invalid/local\t(devel)\n', catalog, target), /differ/)
  assert.throws(() => verifyDependencies(info + '\t=>\tlocal\t(devel)\n', catalog, target), /Replaced/)
  assert.throws(() => verifyDependencies(info + '\tdep\tunreviewed.example/package\tv1.0.0\n', catalog, target), /differ/)
  assert.throws(() => verifyDependencies(info.replace(`go${goVersion}`, 'go0.0.0'), catalog, target), /toolchain/)
  assert.throws(() => verifyDependencies(info.replace(`GOARCH=${config.goarch}`, 'GOARCH=unknown'), catalog, target), /platform/)
})
test('Windows license verification ignores only line endings, retaining copyright checks', () => {
  const reviewed = catalog.components.find(component => component.id === 'native:sqlcipher').files[0].text
  const windows = reviewed.replace(/\n/g, '\r\n')
  assert.doesNotThrow(() => verifyNativeLicense(reviewed, windows))
  assert.doesNotThrow(() => verifyNativeLicense(windows, reviewed))
  assert.throws(() => verifyNativeLicense(windows.replace('ZETETIC', 'altered copyright'), reviewed), /differs/)
})
test('each frontend build must retain the reviewed package set before packaging', () => {
  const inventory = catalog.components.filter(component => component.id.startsWith('npm:'))
  assert.doesNotThrow(() => verifyBundledPackages(catalog, inventory))
  assert.throws(() => verifyBundledPackages(catalog, inventory.slice(1)), /differ/)
  assert.throws(() => verifyBundledPackages(catalog, [...inventory, { name: 'unreviewed', version: '1.0.0' }]), /differ/)
})
test('bundled inventory never silently replaces a second distributed version', () => {
  mkdirSync(join(root, '.oss-cache/tmp'), { recursive: true })
  const directory = mkdtempSync(join(root, '.oss-cache/tmp/bundle-'))
  try {
    const modules = {}
    for (const [path, version] of [['node_modules/library', '1.0.0'], ['node_modules/parent/node_modules/library', '2.0.0']]) {
      const location = join(directory, path)
      mkdirSync(location, { recursive: true })
      writeFileSync(join(location, 'package.json'), JSON.stringify({ name: 'library', version }))
      modules[join(location, 'index.js')] = {}
    }
    assert.deepEqual(bundledPackages({ app: { type: 'chunk', modules } }, directory).map(pkg => pkg.version).sort(), ['1.0.0', '2.0.0'])
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
test('all release and CI builds wait for the single common OSS gate', () => {
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  assert.equal(workflow.match(/node scripts\/oss-notices\.mjs check/g)?.length, 1)
  for (const job of ['go-checks', 'safe-update', 'frontend', 'windows-security', 'docker', 'security-automation', 'parser-fuzz', 'passkey']) {
    assert.ok(workflow.includes(`  ${job}:\n    needs: oss\n`), `${job} bypasses common OSS gate`)
  }
  for (const file of ['release-desktop.yml', 'release-docker.yml']) {
    const child = readFileSync(new URL(`../.github/workflows/${file}`, import.meta.url), 'utf8')
    assert.match(child, /on:\n  workflow_call:/)
    assert.doesNotMatch(child, /^  (push|pull_request|workflow_dispatch):/m)
    assert.doesNotMatch(child, /oss-notices\.mjs check/)
  }
  assert.match(workflow, /OSS_RESULT: \$\{\{ needs\.oss\.result \}\}/)
  assert.match(workflow, /test "\$OSS_RESULT" = success/)
})
