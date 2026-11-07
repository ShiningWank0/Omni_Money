import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { closeSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { bounded, privateAppEnvironment, requireCI, stopProcessGroup, watchExit } from './wails-dev-integration.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const wails = 'github.com/wailsapp/wails/v2'

export function verifyNativeBuild(info, version) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced dependencies are not allowed')
  const dependencies = [...info.matchAll(/^\s*dep\s+(\S+)\s+(\S+)/gm)]
  assert.equal(new Set(dependencies.map(match => match[1])).size, dependencies.length, 'Duplicate dependency')
  assert.equal(dependencies.find(match => match[1] === wails)?.[2], version, 'Packaged Wails differs from the selected version')
  assert.ok(!dependencies.some(match => match[1] === 'github.com/labstack/echo/v4'), 'The production binary unexpectedly contains development Echo')
  const tags = new Set((info.match(/^\s*build\s+-tags=(.+)$/m)?.[1] || '').replace(/^"|"$/g, '').split(','))
  for (const tag of ['production', 'desktop', 'webkit2_41', 'libsqlite3', 'sqlite_omit_load_extension']) assert.ok(tags.has(tag), `Missing production build tag: ${tag}`)
  for (const tag of ['dev', 'bindings', 'server']) assert.ok(!tags.has(tag), `Wrong production build tag: ${tag}`)
  for (const setting of ['CGO_ENABLED=1', 'GOOS=linux', 'GOARCH=amd64']) assert.ok(info.split('\n').some(line => line.trim() === `build\t${setting}` || line.trim() === `build ${setting}`), `Missing build setting: ${setting}`)
}

export function ownedBinary(argument, runnerTemp) {
  assert.ok(argument && isAbsolute(argument), 'Pass the absolute packaged binary')
  assert.ok(lstatSync(argument).isFile() && !lstatSync(argument).isSymbolicLink(), 'The packaged binary must be a regular file')
  const binary = realpathSync(argument)
  const path = relative(realpathSync(runnerTemp), binary)
  assert.ok(path && path !== '..' && !path.startsWith('../') && !isAbsolute(path), 'The packaged binary must be in RUNNER_TEMP')
  return binary
}

function command(program, args, env = process.env) {
  const result = spawnSync(program, args, { cwd: root, env, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
  assert.equal(result.status, 0, result.stderr || String(result.error || `${program} failed`))
  return result.stdout
}

export async function runNativeIntegration(argument) {
  requireCI(process.platform, process.env)
  assert.ok(process.env.DBUS_SESSION_BUS_ADDRESS, 'A private CI D-Bus session is required')
  const binary = ownedBinary(argument, process.env.RUNNER_TEMP)
  const selected = command('go', ['list', '-mod=readonly', '-m', '-f', '{{.Version}}{{if .Replace}} replaced{{end}}', wails]).trim()
  assert.match(selected, /^v2\.\d+\.\d+$/, 'Use the official pinned Wails module')
  const info = command('go', ['version', '-m', binary])
  verifyNativeBuild(info, selected)
  const digest = () => createHash('sha256').update(readFileSync(binary)).digest('hex')
  const before = digest()
  const output = join(root, '.oss-cache/wails-native-results')
  mkdirSync(output, { recursive: true, mode: 0o700 })
  writeFileSync(join(output, 'build-info.txt'), info)
  const directory = mkdtempSync(join(realpathSync(process.env.RUNNER_TEMP), 'omni-wails-native-'))
  const env = privateAppEnvironment(directory, process.env)
  delete env.NO_AT_BRIDGE
  env.GTK_MODULES = 'atk-bridge'
  const password = `native-${randomUUID()}`
  let app, driver, manager, failure, interrupted = false
  const logs = []
  const rounds = []
  const interrupt = () => {
    interrupted = true
    for (const child of [driver, app, manager]) void stopProcessGroup(child).catch(() => {})
  }
  process.once('SIGINT', interrupt)
  process.once('SIGTERM', interrupt)
  const start = (program, args, environment, logName) => {
    const log = openSync(join(output, logName), 'w', 0o600)
    logs.push(log)
    const child = spawn(program, args, { cwd: directory, env: environment, detached: true, stdio: ['ignore', log, log] })
    const exit = watchExit(child)
    exit.catch(() => {})
    return { child, exit }
  }
  try {
    for (const key of ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) mkdirSync(env[key], { recursive: true, mode: 0o700 })
    // D-Bus activation must use this test's isolated directories as well.
    command('dbus-update-activation-environment', ['DISPLAY', 'XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR'], env)
    command('gdbus', ['call', '--session', '--dest', 'org.a11y.Bus', '--object-path', '/org/a11y/bus', '--method', 'org.freedesktop.DBus.Properties.Set', 'org.a11y.Status', 'IsEnabled', '<true>'], env)
    const wm = start('openbox', ['--sm-disable'], env, 'window-manager.log')
    manager = wm.child
    // Wait for an actual window manager; a close request without one proves nothing.
    await bounded((async () => {
      while (manager.exitCode === null && manager.signalCode === null) {
        const ready = spawnSync('wmctrl', ['-m'], { env, encoding: 'utf8', timeout: 2000 })
        if (ready.status === 0) return
        await new Promise(resolve => setTimeout(resolve, 100))
      }
      throw new Error('The private window manager failed to start')
    })(), 10_000, 'The private window manager did not become ready')
    for (const scenario of ['setup', 'reopen']) {
      assert.equal(interrupted, false, 'Integration was interrupted')
      const running = start(binary, [], env, `application-${scenario}.log`)
      app = running.child
      const report = join(output, `ui-${scenario}.json`)
      const automation = start('/usr/bin/python3', [join(root, 'scripts/wails-native-ui.py'), String(app.pid), scenario, report],
        { ...env, OMNI_WAILS_TEST_PASSWORD: password }, `automation-${scenario}.log`)
      driver = automation.child
      assert.deepEqual(await bounded(automation.exit, 120_000, `Native ${scenario} UI test timed out`), { code: 0, signal: null }, `Native ${scenario} UI automation failed; see its log and report`)
      const result = JSON.parse(readFileSync(report, 'utf8'))
      assert.equal(result.passed, true, 'Incomplete native UI evidence')
      assert.deepEqual(await bounded(running.exit, 15_000, 'The native window close did not stop Wails'), { code: 0, signal: null })
      await stopProcessGroup(driver)
      await stopProcessGroup(app)
      rounds.push({ scenario, steps: result.steps, gracefulExit: true })
    }
    assert.equal(digest(), before, 'The tested distribution binary was modified')
    assert.equal(interrupted, false, 'Integration was interrupted')
  } catch (error) { failure = error }
  finally {
    for (const child of [driver, app, manager]) {
      try { await stopProcessGroup(child) } catch (error) { failure ||= error }
    }
    for (const log of logs) closeSync(log)
    try { rmSync(directory, { recursive: true, force: true }) } catch (error) { failure ||= error }
    process.removeListener('SIGINT', interrupt)
    process.removeListener('SIGTERM', interrupt)
    if (interrupted) failure ||= new Error('Integration was interrupted')
    writeFileSync(join(output, 'result.json'), JSON.stringify({ passed: !failure, wails: selected, binarySHA256: before,
      rounds, isolatedDataRemoved: !existsSync(directory), error: failure?.message || null }, null, 2) + '\n')
  }
  if (failure) throw failure
  console.log('Packaged Wails native WebView passed: setup, OSS UI, lock/unlock, restart, normal exit and cleanup')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runNativeIntegration(process.argv[2]).catch(error => { console.error(error); process.exitCode = 1 })
}
