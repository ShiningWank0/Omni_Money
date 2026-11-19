import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { hasOwnedWindow, requireMacCI, verifyMacBuild } from './wails-macos-startup.mjs'

test('native macOS startup cannot launch on a developer machine or another platform', () => {
  const env = { GITHUB_ACTIONS: 'true', RUNNER_TEMP: '/private/ci', DEVELOPER_DIR: '/fixed/Xcode' }
  requireMacCI('darwin', env)
  assert.throws(() => requireMacCI('linux', env))
  assert.throws(() => requireMacCI('darwin', { ...env, GITHUB_ACTIONS: 'false' }))
  const result = spawnSync(process.execPath, ['scripts/wails-macos-startup.mjs', '/missing-binary'], {
    env: { ...process.env, GITHUB_ACTIONS: 'false' }, encoding: 'utf8', timeout: 5000,
  })
  assert.notEqual(result.status, 0)
  assert.doesNotMatch(result.stderr, /ENOENT/)
})

test('a menu, empty inventory or another process cannot establish native window creation', () => {
  assert.equal(hasOwnedWindow([], 123), false)
  assert.equal(hasOwnedWindow([{ pid: 123, layer: 24, width: 100, height: 100 }], 123), false)
  assert.equal(hasOwnedWindow([{ pid: 123, layer: 0, width: 0, height: 100 }], 123), false)
  assert.equal(hasOwnedWindow([{ pid: 123, layer: 0, width: 1280, height: 800 }], 123), true)
  assert.throws(() => hasOwnedWindow([{ pid: 456, layer: 0, width: 1280, height: 800 }], 123))
  assert.throws(() => hasOwnedWindow({}, 123))
})

test('macOS startup rejects dev hosts, another CPU, replaced modules and stale Wails', () => {
  const info = 'binary: go1.27.4\n dep github.com/wailsapp/wails/v3 v3.1.0 h1:test\n build -tags=production,desktop,libsqlite3,sqlite_omit_load_extension\n build CGO_ENABLED=1\n build GOOS=darwin\n build GOARCH=arm64\n'
  verifyMacBuild(info, 'v3.1.0', 'arm64')
  for (const bad of [info.replace('production', 'dev'), info.replace('GOARCH=arm64', 'GOARCH=amd64'),
    info.replace('v3.1.0', 'v3.0.0'), info + ' => local-replacement\n', info.replace('desktop,', 'omni_wails_browser,')]) {
    assert.throws(() => verifyMacBuild(bad, 'v3.1.0', 'arm64'))
  }
})
