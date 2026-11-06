import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { closeSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { createServer, createConnection } from 'node:net'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const modulePaths = ['github.com/wailsapp/wails/v2', 'github.com/labstack/echo/v4']

export function requireCI(platform, env) {
  assert.equal(platform, 'linux', 'The real Wails integration runs only on CI Linux')
  assert.equal(env.GITHUB_ACTIONS, 'true', 'The real Wails integration must not start locally')
  assert.ok(env.RUNNER_TEMP && env.DISPLAY, 'RUNNER_TEMP and the virtual display are required')
}

export function verifyDevBuild(info, expected) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced dependencies are not allowed')
  const dependencies = [...info.matchAll(/^\s*dep\s+(\S+)\s+(\S+)/gm)]
  assert.equal(new Set(dependencies.map(match => match[1])).size, dependencies.length, 'Duplicate dependency')
  for (const path of modulePaths) {
    assert.ok(expected[path], `Missing selected version for ${path}`)
    assert.equal(dependencies.find(match => match[1] === path)?.[2], expected[path], `Built ${path} differs from the project graph`)
  }
  const tags = new Set((info.match(/^\s*build\s+-tags=(.+)$/m)?.[1] || '').replace(/^"|"$/g, '').split(','))
  for (const tag of ['dev', 'desktop', 'webkit2_41', 'libsqlite3', 'sqlite_omit_load_extension']) assert.ok(tags.has(tag), `Missing integration build tag: ${tag}`)
  for (const tag of ['production', 'bindings', 'server']) assert.ok(!tags.has(tag), `Wrong integration build tag: ${tag}`)
  assert.match(info, /^\s*build\s+CGO_ENABLED=1$/m)
  assert.match(info, /^\s*build\s+GOOS=linux$/m)
}

export function privateAppEnvironment(directory, inherited) {
  const env = { ...inherited, XDG_DATA_HOME: join(directory, 'data'), XDG_CONFIG_HOME: join(directory, 'config'),
    XDG_CACHE_HOME: join(directory, 'cache'), XDG_RUNTIME_DIR: join(directory, 'runtime'), TMPDIR: join(directory, 'tmp'),
    GDK_BACKEND: 'x11', WEBKIT_DISABLE_COMPOSITING_MODE: '1', LIBGL_ALWAYS_SOFTWARE: '1' }
  // These upstream Wails environment variables take precedence over CLI flags.
  for (const key of ['devserver', 'assetdir', 'frontenddevserverurl', 'loglevel', 'GITHUB_TOKEN', 'GH_TOKEN']) delete env[key]
  return env
}

export function watchExit(child) {
  return new Promise((resolve, reject) => {
    child.once('error', reject)
    child.once('exit', (code, signal) => resolve({ code, signal }))
  })
}

export async function bounded(promise, milliseconds, message) {
  let timer
  try {
    return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), milliseconds) })])
  } finally { clearTimeout(timer) }
}

function groupExists(pid) {
  try { process.kill(-pid, 0); return true } catch (error) {
    if (error.code === 'ESRCH') return false
    throw error
  }
}

export async function stopProcessGroup(child) {
  if (!child?.pid || !groupExists(child.pid)) return
  for (const signal of ['SIGTERM', 'SIGKILL']) {
    try { process.kill(-child.pid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
    for (let attempt = 0; attempt < 30; attempt++) {
      if (!groupExists(child.pid)) return
      await delay(100)
    }
  }
  throw new Error('Wails process group did not stop')
}

async function unusedPort() {
  const server = createServer()
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const port = server.address().port
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  return port
}

async function portIsOpen(port) {
  return new Promise(resolve => {
    const socket = createConnection({ host: '127.0.0.1', port })
    const finish = value => { socket.destroy(); resolve(value) }
    socket.once('connect', () => finish(true))
    socket.once('error', () => finish(false))
    socket.setTimeout(1000, () => finish(true))
  })
}

async function waitForOwnedServer(url, marker, child) {
  const deadline = Date.now() + 45_000
  while (Date.now() < deadline) {
    assert.equal(child.exitCode, null, 'Wails exited before serving the application')
    assert.equal(child.signalCode, null, 'Wails was terminated before serving the application')
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1000) })
      if (response.status === 200 && await response.text() === marker) return
    } catch { /* The owned listener may not be ready yet. */ }
    await delay(200)
  }
  throw new Error('Wails/Echo did not serve this test instance before the deadline')
}

function go(args) {
  const result = spawnSync('go', args, { cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
  assert.equal(result.status, 0, result.stderr || String(result.error || 'Go inspection failed'))
  return result.stdout
}

export async function runIntegration(binaryArgument) {
  requireCI(process.platform, process.env)
  assert.ok(binaryArgument && isAbsolute(binaryArgument), 'Pass the absolute CI-built development binary')
  assert.ok(!lstatSync(binaryArgument).isSymbolicLink(), 'The test binary must not be a symlink')
  const binary = realpathSync(binaryArgument)
  const runnerTemp = realpathSync(process.env.RUNNER_TEMP)
  const binaryRelative = relative(runnerTemp, binary)
  assert.ok(binaryRelative && !binaryRelative.startsWith('..') && !isAbsolute(binaryRelative), 'The test binary must be in RUNNER_TEMP')
  const expected = Object.fromEntries(go(['list', '-mod=readonly', '-m', '-f', '{{.Path}} {{.Version}}{{if .Replace}} replaced{{end}}', ...modulePaths])
    .trim().split('\n').map(line => {
      const fields = line.trim().split(/\s+/)
      assert.equal(fields.length, 2, 'Unpinned or replaced selected module')
      return fields
    }))
  const info = go(['version', '-m', binary])
  verifyDevBuild(info, expected)
  const output = join(root, '.oss-cache/wails-dev-results')
  mkdirSync(output, { recursive: true, mode: 0o700 })
  writeFileSync(join(output, 'build-info.txt'), info)
  const directory = mkdtempSync(join(runnerTemp, 'omni-wails-dev-'))
  let child, exit, context, failure, port, log, owned = false, interrupted = false, gracefulExit = false, listenerClosed = false
  let sent = 0, received = 0
  const interrupt = () => {
    interrupted = true
    void context?.close().catch(() => {})
    void stopProcessGroup(child).catch(() => {})
  }
  process.once('SIGINT', interrupt)
  process.once('SIGTERM', interrupt)
  try {
    const env = privateAppEnvironment(directory, process.env)
    for (const key of ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) mkdirSync(env[key], { recursive: true, mode: 0o700 })
    const assets = join(directory, 'assets')
    cpSync(join(root, 'frontend/dist'), assets, { recursive: true })
    const marker = randomUUID()
    const markerName = `integration-${marker}.txt`
    writeFileSync(join(assets, markerName), marker)
    port = await unusedPort()
    assert.equal(interrupted, false, 'Integration was interrupted')
    const baseURL = `http://127.0.0.1:${port}`
    log = openSync(join(output, 'application.log'), 'w', 0o600)
    child = spawn(binary, ['-devserver', `127.0.0.1:${port}`, '-assetdir', assets, '-loglevel', 'info'],
      { cwd: root, env, detached: true, stdio: ['ignore', log, log] })
    exit = watchExit(child)
    // Observe startup errors without leaving an unhandled rejected promise.
    exit.catch(() => {})
    await waitForOwnedServer(`${baseURL}/${markerName}`, marker, child)
    owned = true
    const { chromium } = createRequire(join(root, 'frontend/package.json'))('@playwright/test')
    context = await chromium.launchPersistentContext(join(directory, 'browser-profile'), { headless: true, locale: 'ja-JP', timeout: 30_000, env })
    assert.equal(interrupted, false, 'Integration was interrupted')
    const page = await context.newPage()
    page.setDefaultTimeout(20_000)
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    page.on('websocket', socket => {
      if (socket.url().startsWith(`${baseURL.replace('http:', 'ws:')}/wails/ipc`)) {
        socket.on('framesent', () => sent++)
        socket.on('framereceived', () => received++)
      }
    })
    const response = await page.goto(baseURL, { waitUntil: 'domcontentloaded', timeout: 30_000 })
    assert.equal(response.status(), 200)
    await page.waitForFunction(() => typeof window.go?.main?.App?.GetDesktopVaultStatus === 'function')
    const status = await bounded(page.evaluate(() => window.go.main.App.GetDesktopVaultStatus()), 15_000, 'The real Go binding did not reply')
    assert.deepEqual(status, { configured: false, unlocked: false, legacy_migration_required: false, role: '' })
    assert.ok(sent > 0 && received > 0, 'The actual Wails WebSocket IPC must exchange frames')
    await page.locator('.desktop-vault-gate').waitFor({ state: 'visible' })
    await page.getByRole('heading', { name: 'Omni Money', exact: true }).waitFor({ state: 'visible' })
    await page.getByRole('button', { name: 'オープンソースライセンス', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'visible' })
    await page.getByRole('button', { name: 'ライセンスを閉じる', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'detached' })
    assert.deepEqual(errors, [], 'The real application reported a browser error')
    await page.evaluate(() => window.runtime.Quit())
    assert.deepEqual(await bounded(exit, 15_000, 'Wails did not exit after runtime.Quit'), { code: 0, signal: null })
    gracefulExit = true
    assert.equal(interrupted, false, 'Integration was interrupted')
  } catch (error) { failure = error }
  finally {
    try { if (context) await bounded(context.close(), 10_000, 'Browser cleanup timed out') } catch (error) { failure ||= error }
    try { await stopProcessGroup(child) } catch (error) { failure ||= error }
    if (log !== undefined) closeSync(log)
    if (owned) {
      try {
        assert.equal(await portIsOpen(port), false, 'The owned Wails listener remained open')
        listenerClosed = true
      } catch (error) { failure ||= error }
    }
    try { rmSync(directory, { recursive: true, force: true }) } catch (error) { failure ||= error }
    process.removeListener('SIGINT', interrupt)
    process.removeListener('SIGTERM', interrupt)
    if (interrupted) failure ||= new Error('Integration was interrupted')
    if (!existsSync(directory) && !failure) assert.ok(gracefulExit && listenerClosed && sent > 0 && received > 0, 'Incomplete integration evidence')
    writeFileSync(join(output, 'result.json'), JSON.stringify({ passed: !failure, modules: expected, websocketFrames: { sent, received }, gracefulExit, listenerClosed, isolatedDataRemoved: !existsSync(directory),
      error: failure?.message || null }, null, 2) + '\n')
  }
  if (failure) throw failure
  console.log('Wails/Echo development integration passed: real UI, Go IPC, graceful exit and cleanup')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runIntegration(process.argv[2]).catch(error => { console.error(error); process.exitCode = 1 })
}
