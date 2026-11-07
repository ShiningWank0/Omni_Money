import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { ownedBinary, verifyNativeBuild } from './wails-native-integration.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
const version = 'v2.17.0'
const info = `binary: go1.27.3
 dep github.com/wailsapp/wails/v2 ${version} h1:fixture
 build -tags=production,desktop,webkit2_41,libsqlite3,sqlite_omit_load_extension
 build CGO_ENABLED=1
 build GOOS=linux
 build GOARCH=amd64
`

test('the native harness requires the actual selected production Wails binary', () => {
  verifyNativeBuild(info, version)
  for (const malformed of [
    info.replace(version, 'v2.16.0'),
    info.replace('production', 'dev'),
    info.replace('desktop,', 'bindings,'),
    info.replace('libsqlite3,', ''),
    info.replace('CGO_ENABLED=1', 'CGO_ENABLED=0'),
    info.replace('GOOS=linux', 'GOOS=darwin'),
    info.replace('GOARCH=amd64', 'GOARCH=arm64'),
    info + '\t=> replacement v0.0.0\n',
    info + ' dep github.com/labstack/echo/v4 v4.16.3\n',
    info + ` dep github.com/wailsapp/wails/v2 ${version}\n`,
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

test('manual browser reproduction stays strict while packaged native UI is automatic', () => {
  const ci = readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8')
  const manual = readFileSync(join(root, '.github/workflows/wails-dev-integration.yml'), 'utf8')
  const desktop = readFileSync(join(root, '.github/workflows/release-desktop.yml'), 'utf8')
  assert.doesNotMatch(ci, /node scripts\/wails-dev-integration\.mjs/)
  assert.match(manual, /^  workflow_dispatch:/m)
  assert.doesNotMatch(manual, /^  (pull_request|push|schedule):/m)
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
