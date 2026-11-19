import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import test from 'node:test'
import { hasOwnedWindow, requireMacCI, stopOwnedMacApplication, verifyMacBuild, verifyMacTeardown } from './wails-macos-startup.mjs'
import { bounded, watchExit } from './wails-dev-integration.mjs'

test('a native crash or nonzero exit can never be accepted as macOS teardown', () => {
  verifyMacTeardown({ code: 0, signal: null })
  verifyMacTeardown({ code: null, signal: 'SIGTERM' })
  for (const result of [null, { code: 7, signal: null }, { code: null, signal: 'SIGSEGV' },
    { code: null, signal: 'SIGABRT' }, { code: null, signal: 'SIGKILL' }, { code: null, signal: null }]) {
    assert.throws(() => verifyMacTeardown(result))
  }
})

test('macOS cleanup reaps only the owned child and never hides a signal permission error', async t => {
  const child = spawn(process.execPath, ['-e', 'process.stdout.write("ready");setInterval(()=>{},1000)'], { stdio: ['ignore', 'pipe', 'pipe'] })
  const exited = watchExit(child)
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
    await exited
  })
  await bounded(once(child.stdout, 'data'), 5000, 'Owned child did not start')
  const result = await stopOwnedMacApplication(child, exited)
  assert.equal(result.signal, 'SIGTERM')
  child.kill = () => assert.fail('An already reaped PID must not be signalled')
  assert.deepEqual(await stopOwnedMacApplication(child, exited), result)
  const permission = Object.assign(new Error('EPERM fixture'), { code: 'EPERM' })
  await assert.rejects(stopOwnedMacApplication({ pid: 123, exitCode: null, signalCode: null, kill() { throw permission } }, new Promise(() => {})), { code: 'EPERM' })
})

test('an unresponsive owned Unix child is reaped but still fails cleanup', { skip: process.platform === 'win32' }, async t => {
  const child = spawn(process.execPath, ['-e', 'process.on("SIGTERM",()=>{});process.stdout.write("ready");setInterval(()=>{},1000)'], { stdio: ['ignore', 'pipe', 'pipe'] })
  const exited = watchExit(child)
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
    await exited
  })
  await bounded(once(child.stdout, 'data'), 5000, 'Owned child did not start')
  await assert.rejects(stopOwnedMacApplication(child, exited, 50), /did not stop/)
  assert.equal(child.signalCode, 'SIGKILL')
})

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
