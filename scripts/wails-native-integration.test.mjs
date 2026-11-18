import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { mountsWithin, ownedBinary, removeSessionData, verifyNativeBuild } from './wails-native-integration.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))

test('checkout keeps generated binding bytes under Windows autocrlf and detects actual edits', t => {
  const directory = mkdtempSync(join(root, '.wails-checkout-test-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const git = args => {
    const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8', timeout: 5000 })
    assert.equal(result.status, 0, result.stderr || result.error?.message)
    return result.stdout
  }
  git(['init', '--quiet'])
  git(['config', 'core.autocrlf', 'true'])
  writeFileSync(join(directory, '.gitattributes'), readFileSync(join(root, '.gitattributes')))
  mkdirSync(join(directory, 'frontend/bindings/events'), { recursive: true })
  const path = 'frontend/bindings/events/index.js'
  const expected = readFileSync(join(root, path))
  writeFileSync(join(directory, path), expected)
  git(['add', '.gitattributes', path])
  rmSync(join(directory, path))
  git(['checkout-index', '--force', '--', path])
  assert.deepEqual(readFileSync(join(directory, path)), expected)
  writeFileSync(join(directory, path), Buffer.concat([expected, Buffer.from('// actual edit\n')]))
  assert.notDeepEqual(readFileSync(join(directory, path)), expected)
  assert.notEqual(git(['diff', '--', path]), '')
})

test('session cleanup waits for portal mounts and propagates removal errors', async t => {
  const directory = mkdtempSync(join(root, '.wails-cleanup-test-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const mount = path => `30 20 0:26 / ${path} rw,nosuid - fuse.portal portal rw\n`
  assert.deepEqual(mountsWithin(mount('/private/test/doc') + mount('/private/testing/doc'), '/private/test'), ['/private/test/doc'])
  assert.deepEqual(mountsWithin(mount('/private/test\\040space/doc'), '/private/test space'), ['/private/test space/doc'])
  assert.throws(() => mountsWithin('invalid', directory), /mount inventory/)
  let inspections = 0, removed = false
  await removeSessionData(directory, () => ++inspections === 1 ? mount(directory + '/runtime/doc') : '', (...args) => {
    assert.ok(inspections >= 2, 'Removal traversed a live portal mount')
    removed = true; rmSync(...args)
  })
  assert.equal(removed, true)
  mkdirSync(directory)
  await assert.rejects(removeSessionData(directory, () => '', () => { throw new Error('EPERM fixture') }), /EPERM fixture/)
  await assert.rejects(removeSessionData(directory, () => '', () => {}), /data remains/)
  await assert.rejects(removeSessionData(directory, () => mount(directory + '/runtime/doc'), () => {
    assert.fail('Removal must not traverse a persistent mount')
  }, 0), /mounts did not stop/)
})

test('Windows WebView option tests run after the real frontend and application build', () => {
  const workflow = readFileSync(join(root, '.github/workflows/release-desktop.yml'), 'utf8')
  const windows = workflow.slice(workflow.indexOf('- name: Build application on Windows'))
  const build = windows.indexOf('"$WAILS_EXE" build')
  const test = windows.indexOf("-run '^TestFixedCIWebview'")
  assert.ok(build >= 0 && test > build, 'Go embed tests require the real frontend build first')
})
const version = 'v3.1.0'
const info = `binary: go1.27.3
 dep github.com/wailsapp/wails/v3 ${version} h1:fixture
 build -tags=production,desktop,gtk3,libsqlite3,sqlite_omit_load_extension
 build CGO_ENABLED=1
 build GOOS=linux
 build GOARCH=amd64
`

test('the native harness requires the actual selected production Wails binary', () => {
  verifyNativeBuild(info, version)
  for (const malformed of [
    info.replace(version, 'v3.0.0'),
    info.replace('production', 'dev'),
    info.replace('desktop,', 'bindings,'),
    info.replace('libsqlite3,', ''),
    info.replace('CGO_ENABLED=1', 'CGO_ENABLED=0'),
    info.replace('GOOS=linux', 'GOOS=darwin'),
    info.replace('GOARCH=amd64', 'GOARCH=arm64'),
    info + '\t=> replacement v0.0.0\n',
    info.replace('production,desktop', 'production,desktop,purego'),
    info + ` dep github.com/wailsapp/wails/v3 ${version}\n`,
  ]) assert.throws(() => verifyNativeBuild(malformed, version))
})

test('only a regular packaged file inside the owned runner directory can be launched', t => {
  const cache = join(root, '.oss-cache')
  const existed = existsSync(cache)
  mkdirSync(cache, { recursive: true })
  const directory = mkdtempSync(join(cache, 'wails-native-unit-'))
  t.after(() => {
    rmSync(directory, { recursive: true, force: true })
    if (!existed) { try { rmdirSync(cache) } catch (error) { if (error.code !== 'ENOTEMPTY') throw error } }
  })
  const binary = join(directory, 'omni_money')
  writeFileSync(binary, 'not executed')
  assert.equal(ownedBinary(binary, directory), binary)
  assert.throws(() => ownedBinary(directory, directory), /regular file/)
  assert.throws(() => ownedBinary(binary, join(root, 'scripts')), /RUNNER_TEMP/)
  assert.throws(() => ownedBinary('omni_money', directory), /absolute/)
  symlinkSync(binary, join(directory, 'linked'))
  assert.throws(() => ownedBinary(join(directory, 'linked'), directory), /regular file/)
})

test('direct invocation refuses local execution before inspecting or launching an app', () => {
  const result = spawnSync(process.execPath, ['scripts/wails-native-integration.mjs', '/missing-app'], {
    cwd: root, encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'false' }, timeout: 5000,
  })
  assert.notEqual(result.status, 0)
  assert.match(result.stderr, /CI Linux|must not start locally/)
  assert.doesNotMatch(result.stderr, /ENOENT/)
})

test('native automation rejects other PIDs, ambiguous windows and local execution', () => {
  const source = `
import importlib.util
spec = importlib.util.spec_from_file_location('native_ui', 'scripts/wails-native-ui.py')
ui = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ui)
listing = '0x001 0 42 runner Omni Money\\n0x002 0 99 runner Other application\\n'
assert ui.owned_window_id(listing, 42) == '0x001'
for invalid, pid in [(listing, 99), (listing, 1), (listing + listing, 42)]:
    try: ui.owned_window_id(invalid, pid)
    except RuntimeError: pass
    else: raise AssertionError('unsafe window accepted')
for platform, env in [('darwin', {}), ('linux', {}), ('linux', {'GITHUB_ACTIONS': 'true'})]:
    try: ui.require_ci(platform, env)
    except RuntimeError: pass
    else: raise AssertionError('unsafe environment accepted')
ui.require_ci('linux', {'GITHUB_ACTIONS': 'true', 'RUNNER_TEMP': '/ci', 'DISPLAY': ':99', 'DBUS_SESSION_BUS_ADDRESS': 'private'})
`
  const result = spawnSync('python3', ['-B', '-c', source], { cwd: root, encoding: 'utf8', timeout: 5000 })
  assert.equal(result.status, 0, result.stderr || result.error?.message)
})

test('native static-text lookup and privacy regression tests pass without a real GUI', () => {
  const result = spawnSync('python3', ['-B', 'scripts/wails-native-ui.test.py'], {
    cwd: root, encoding: 'utf8', timeout: 10_000,
  })
  assert.equal(result.status, 0, result.stderr || result.error?.message)
})

test('known upstream development failures stay in strict manual diagnostics while packaged native UI is required', () => {
  const ci = readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8')
  const manual = readFileSync(join(root, '.github/workflows/wails-dev-integration.yml'), 'utf8')
  const desktop = readFileSync(join(root, '.github/workflows/release-desktop.yml'), 'utf8')
  assert.doesNotMatch(ci, /node scripts\/wails-dev-integration\.mjs/)
  assert.match(manual, /^  workflow_dispatch:/m)
  assert.doesNotMatch(ci, /wails-echo|wails-dev-integration\.yml/)
  assert.doesNotMatch(manual, /^  (pull_request|push|schedule|workflow_call):/m)
  assert.match(manual, /node scripts\/wails-dev-integration\.mjs/)
  const packaged = desktop.split('      - name: Verify packaged Linux app\n')[1]?.split('\n      - name: ')[0]
  assert.ok(packaged)
  assert.match(packaged, /tar xzf/)
  assert.match(packaged, /oss-notices\.mjs verify desktop-linux-amd64/)
  assert.match(packaged, /node scripts\/wails-native-integration\.mjs "\$destination\/omni_money"/)
  assert.match(packaged, /--self-test-sqlcipher/)
  for (const source of [manual, packaged]) assert.doesNotMatch(source, /continue-on-error|\|\| true/)
  assert.match(manual, /Issue #299/)
  assert.match(packaged, /Issue #299/)
})

test('Desktop publishing requires successful production builds and attestation', () => {
  const desktop = readFileSync(join(root, '.github/workflows/release-desktop.yml'), 'utf8')
  const ci = readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8')
  assert.doesNotMatch(desktop, /compatibility-gate|wails_echo_result/)
  assert.match(ci, /needs: \[oss, release-plan\]/)
  assert.match(desktop, /needs: \[prepare, build, attest\]/)
  assert.match(desktop, /node scripts\/wails-dev-build\.mjs/)
  for (const job of ['attest', 'release']) {
    const block = desktop.split(`  ${job}:\n`)[1]?.split('    permissions:')[0]
    assert.match(block, /needs: \[prepare, build(?:, attest)?\]/)
    assert.match(block, /github.event_name == 'push'/)
    assert.match(block, /needs.build.result == 'success'/)
    if (job === 'release') assert.match(block, /needs.attest.result == 'success'/)
  }
})
