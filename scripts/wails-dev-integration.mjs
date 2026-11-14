import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { closeSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { request } from 'node:http'
import { createHash } from 'node:crypto'
import { readFileSync, symlinkSync } from 'node:fs'
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

export function verifyDevBuild(info, expected, platform = 'linux/amd64') {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced dependencies are not allowed')
  const dependencies = [...info.matchAll(/^\s*dep\s+(\S+)\s+(\S+)/gm)]
  assert.equal(new Set(dependencies.map(match => match[1])).size, dependencies.length, 'Duplicate dependency')
  for (const path of modulePaths) {
    assert.ok(expected[path], `Missing selected version for ${path}`)
    assert.equal(dependencies.find(match => match[1] === path)?.[2], expected[path], `Built ${path} differs from the project graph`)
  }
  const tags = new Set((info.match(/^\s*build\s+-tags=(.+)$/m)?.[1] || '').replace(/^"|"$/g, '').split(','))
  const [goos, goarch] = platform.split('/')
  assert.ok(['linux', 'darwin', 'windows'].includes(goos) && ['amd64', 'arm64'].includes(goarch), 'Unsupported development build platform')
  for (const tag of ['dev', 'desktop', 'libsqlite3', 'sqlite_omit_load_extension', ...(goos === 'linux' ? ['webkit2_41'] : [])]) assert.ok(tags.has(tag), `Missing integration build tag: ${tag}`)
  for (const tag of ['production', 'bindings', 'server']) assert.ok(!tags.has(tag), `Wrong integration build tag: ${tag}`)
  assert.match(info, /^\s*build\s+CGO_ENABLED=1$/m)
  assert.match(info, new RegExp(`^\\s*build\\s+GOOS=${goos}$`, 'm'))
  assert.match(info, new RegExp(`^\\s*build\\s+GOARCH=${goarch}$`, 'm'))
}

export function privateAppEnvironment(directory, inherited) {
  const env = { ...inherited, XDG_DATA_HOME: join(directory, 'data'), XDG_CONFIG_HOME: join(directory, 'config'),
    XDG_CACHE_HOME: join(directory, 'cache'), XDG_RUNTIME_DIR: join(directory, 'runtime'), TMPDIR: join(directory, 'tmp'),
    GDK_BACKEND: 'x11', WEBKIT_DISABLE_COMPOSITING_MODE: '1', LIBGL_ALWAYS_SOFTWARE: '1' }
  // These upstream Wails environment variables take precedence over CLI flags.
  for (const key of ['devserver', 'assetdir', 'frontenddevserverurl', 'loglevel', 'GITHUB_TOKEN', 'GH_TOKEN', 'WAILS_DEV_ALLOWED_HOSTS']) delete env[key]
  return env
}

export function browserErrorDetails(error, stage) {
  return { stage, name: error.name, message: error.message, stack: error.stack || null }
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

async function waitForOwnedServer(url, marker, child, timeout = 45_000) {
  const deadline = Date.now() + timeout
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

function go(args, cwd = root) {
  const result = spawnSync('go', args, { cwd, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
  assert.equal(result.status, 0, result.stderr || String(result.error || 'Go inspection failed'))
  return result.stdout
}

// A real HTTP upgrade, without a browser's automatically attached cookies.
export function probeHTTP(baseURL, path, headers = {}, upgrade = false) {
  return new Promise((resolve, reject) => {
    const req = request(new URL(path, baseURL), { headers: { ...headers, ...(upgrade ? {
      Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13',
      'Sec-WebSocket-Key': Buffer.from(randomUUID().replaceAll('-', '').slice(0, 16)).toString('base64'),
    } : {}) } })
    req.setTimeout(5000, () => req.destroy(new Error('HTTP probe timed out')))
    req.once('error', reject)
    req.once('upgrade', (response, socket) => { socket.destroy(); resolve({ status: response.statusCode, headers: response.headers }) })
    req.once('response', response => { response.resume(); response.once('end', () => resolve({ status: response.statusCode, headers: response.headers })) })
    req.end()
  })
}

export function verifyEvidence(result, errors, mode, commit) {
  assert.equal(result.mode, mode)
  assert.ok(commit && result.commit === commit, 'Evidence must identify this checkout')
  assert.equal(result.passed, true, 'Integration failed; inspect result.json and browser-errors.json')
  assert.deepEqual(result.failures, [])
  assert.deepEqual(errors, [], 'Browser errors must fail compatibility')
  for (const check of ['http', 'goIPC', 'licenses', 'security', 'gracefulExit', 'browserClosed', 'processStopped', 'listenerClosed', 'isolatedDataRemoved',
    ...(mode === 'vite' ? ['viteProxy', 'hmr', 'cliBuild', 'viteListenerClosed', 'cliProcessesStopped'] : ['reconnect', 'capabilityRotated'])]) {
    assert.equal(result.checks[check], true, `Incomplete evidence: ${check}`)
  }
  assert.ok(result.websocketFrames.sent > 0 && result.websocketFrames.received > 0)
  assert.match(result.binarySHA256, /^[a-f0-9]{64}$/)
}

// The CLI gives its frontend watcher a separate process group. Remember only
// descendants of our own CLI so failure cleanup can stop that group as well.
function rememberDescendants(child, groups) {
  if (!child?.pid) return
  const snapshot = spawnSync('ps', ['-e', '-o', 'pid=,ppid=,pgid='], { encoding: 'utf8', timeout: 2000 })
  assert.equal(snapshot.status, 0, 'Cannot inspect owned CLI descendants')
  const rows = snapshot.stdout.trim().split('\n').map(line => line.trim().split(/\s+/).map(Number))
  const owned = new Set([child.pid])
  let changed = true
  while (changed) {
    changed = false
    for (const [pid, ppid, pgid] of rows) if (owned.has(ppid) && !owned.has(pid)) {
      owned.add(pid); groups.add(pgid); changed = true
    }
  }
}

export async function runIntegration(binaryArgument, mode = 'assets') {
  requireCI(process.platform, process.env)
  assert.ok(['assets', 'vite'].includes(mode), 'Unknown integration mode')
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
  const result = { mode, commit: process.env.GITHUB_SHA, modules: expected, node: process.version,
    binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex'),
    checks: {}, failures: [], websocketFrames: { sent: 0, received: 0 } }
  const browserErrors = [], groups = new Set()
  let child, exit, context, port, vitePort, log, tracker, interrupted = false, stage = 'starting'
  const check = async (name, callback) => {
    stage = name
    try { await callback(); result.checks[name] = true }
    catch (error) { result.failures.push({ stage, message: error.message, stack: error.stack }); result.checks[name] = false }
  }
  const interrupt = () => { interrupted = true; void context?.close().catch(() => {}); void stopProcessGroup(child).catch(() => {}) }
  process.once('SIGINT', interrupt)
  process.once('SIGTERM', interrupt)
  try {
    const env = privateAppEnvironment(directory, process.env)
    for (const key of ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) mkdirSync(env[key], { recursive: true, mode: 0o700 })
    port = await unusedPort()
    const baseURL = `http://127.0.0.1:${port}`
    const marker = randomUUID(), markerName = `integration-${marker}.txt`
    let project = root, assets = join(directory, 'assets'), appSource
    let executable = binary, args = ['-devserver', `127.0.0.1:${port}`, '-assetdir', assets, '-loglevel', 'info']
    if (mode === 'vite') {
      project = join(directory, 'project')
      cpSync(root, project, { recursive: true, filter: source => {
        const parts = relative(root, source).split('/')
        return !parts.some(part => ['.git', '.oss-cache', 'node_modules', '.codex', '.agents', '.aws'].includes(part) || part.startsWith('.env')) &&
          !relative(root, source).startsWith('build/bin')
      } })
      symlinkSync(join(root, 'frontend/node_modules'), join(project, 'frontend/node_modules'), 'dir')
      do { vitePort = await unusedPort() } while (vitePort === port)
      const config = join(project, 'frontend/vite.config.js')
      const configSource = readFileSync(config, 'utf8')
      assert.equal(configSource.split('  server: {').length, 2, 'Expected the real Vite server configuration')
      writeFileSync(config, configSource.replace('  server: {', `  server: {\n    port: ${vitePort},`))
      appSource = join(project, 'frontend/src/App.vue')
      const vue = readFileSync(appSource, 'utf8')
      assert.ok(vue.includes('<div id="app">'), 'Expected the real application root')
      writeFileSync(appSource, vue.replace('<div id="app">', `<div id="app"><span data-ci-hmr>${marker}</span>`))
      assets = join(project, 'frontend/dist')
      writeFileSync(join(project, 'frontend', markerName), marker)
      const cliProject = JSON.parse(readFileSync(join(project, 'wails.json'), 'utf8'))
      assert.equal(cliProject['frontend:dev:serverUrl'], 'auto')
      executable = join(go(['env', 'GOPATH']).trim(), 'bin/wails')
      args = ['dev', '-s', '-m', '-nosyncgomod', '-nocolor', '-viteservertimeout', '60', '-devserver', `127.0.0.1:${port}`,
        '-assetdir', assets, '-tags', 'desktop,webkit2_41,libsqlite3,sqlite_omit_load_extension']
      result.frontendWatcher = cliProject['frontend:dev:watcher']
    } else {
      cpSync(join(root, 'frontend/dist'), assets, { recursive: true })
      writeFileSync(join(assets, markerName), marker)
    }
    log = openSync(join(output, 'application.log'), 'w', 0o600)
    const start = () => {
      assert.equal(interrupted, false, 'Integration was interrupted')
      child = spawn(executable, args, { cwd: project, env, detached: true, stdio: ['ignore', log, log] })
      exit = watchExit(child); exit.catch(() => {})
    }
    start()
    if (mode === 'vite') tracker = setInterval(() => { try { rememberDescendants(child, groups) } catch (error) { result.trackerError = error.message } }, 300)
    await waitForOwnedServer(`${baseURL}/${markerName}`, marker, child, mode === 'vite' ? 240_000 : 45_000)
    result.checks.http = true
    if (mode === 'vite') {
      result.checks.viteProxy = true
      const cliBinary = join(project, 'build/bin/omni_money-dev-linux-amd64')
      const builtInfo = go(['version', '-m', cliBinary], project)
      verifyDevBuild(builtInfo, expected)
      writeFileSync(join(output, 'cli-build-info.txt'), builtInfo)
      result.cliBinarySHA256 = createHash('sha256').update(readFileSync(cliBinary)).digest('hex')
      result.checks.cliBuild = true
    }
    const { chromium } = createRequire(join(root, 'frontend/package.json'))('@playwright/test')
    context = await chromium.launchPersistentContext(join(directory, 'browser-profile'), { headless: true, locale: 'ja-JP', timeout: 30_000, env })
    const page = await context.newPage()
    page.setDefaultTimeout(15_000)
    const cdp = await context.newCDPSession(page)
    await cdp.send('Network.enable')
    const ipcRequests = new Set()
    let ipcConnections = 0
    cdp.on('Network.webSocketCreated', event => {
      if (event.url === `${baseURL.replace('http:', 'ws:')}/wails/ipc`) ipcRequests.add(event.requestId)
    })
    cdp.on('Network.webSocketHandshakeResponseReceived', event => {
      if (ipcRequests.has(event.requestId) && event.response.status === 101) ipcConnections++
    })
    page.on('pageerror', error => browserErrors.push(browserErrorDetails(error, stage)))
    page.on('websocket', socket => {
      if (socket.url().startsWith(`${baseURL.replace('http:', 'ws:')}/wails/ipc`)) {
        socket.on('framesent', () => result.websocketFrames.sent++)
        socket.on('framereceived', () => result.websocketFrames.received++)
      }
    })
    stage = 'loading application'
    const response = await page.goto(baseURL, { waitUntil: 'domcontentloaded', timeout: 30_000 })
    assert.equal(response.status(), 200)
    await page.waitForFunction(() => typeof window.go?.main?.App?.GetDesktopVaultStatus === 'function')
    const callGo = async () => {
      const status = await bounded(page.evaluate(() => window.go.main.App.GetDesktopVaultStatus()), 15_000, 'The real Go binding did not reply')
      assert.deepEqual(status, { configured: false, unlocked: false, legacy_migration_required: false, role: '' })
      assert.ok(result.websocketFrames.sent > 0 && result.websocketFrames.received > 0)
    }
    await check('goIPC', callGo)
    await check('licenses', async () => {
      await page.locator('.desktop-vault-gate').waitFor({ state: 'visible' })
      await page.getByRole('heading', { name: 'Omni Money', exact: true }).waitFor({ state: 'visible' })
      await page.getByRole('button', { name: 'オープンソースライセンス', exact: true }).click()
      await page.getByRole('dialog').waitFor({ state: 'visible' })
      await page.getByRole('button', { name: 'ライセンスを閉じる', exact: true }).click()
      await page.getByRole('dialog').waitFor({ state: 'detached' })
    })
    const getCapability = async () => {
      const cookie = (await context.cookies(baseURL)).find(c => c.name === `wails_ipc_capability_${port}`)
      assert.ok(cookie?.value && cookie.httpOnly && cookie.sameSite === 'Strict', 'Expected a protected capability cookie')
      return cookie
    }
    await check('security', async () => {
      const cookie = await getCapability(), valid = `${cookie.name}=${cookie.value}`
      const deniedHost = await probeHTTP(baseURL, '/', { Host: `attacker.invalid:${port}` })
      assert.equal(deniedHost.status, 403)
      assert.equal(deniedHost.headers['set-cookie'], undefined)
      for (const headers of [
        { Origin: baseURL }, // missing capability
        { Origin: baseURL, Cookie: `${cookie.name}=invalid` },
        { Origin: 'http://attacker.invalid', Cookie: valid },
        { Cookie: valid }, // missing Origin
        { Host: `attacker.invalid:${port}`, Origin: `http://attacker.invalid:${port}`, Cookie: valid },
      ]) assert.equal((await probeHTTP(baseURL, '/wails/ipc', headers, true)).status, 403)
      assert.equal((await probeHTTP(baseURL, '/wails/ipc', { Origin: baseURL, Cookie: valid }, true)).status, 101)
    })
    if (mode === 'vite') await check('hmr', async () => {
      assert.equal(await page.locator('[data-ci-hmr]').textContent(), marker)
      let navigations = 0
      const onNavigate = frame => { if (frame === page.mainFrame()) navigations++ }
      page.on('framenavigated', onNavigate)
      try {
        writeFileSync(appSource, readFileSync(appSource, 'utf8').replace(marker, `${marker}-updated`))
        await page.waitForFunction(value => document.querySelector('[data-ci-hmr]')?.textContent === value, `${marker}-updated`)
        assert.equal(navigations, 0, 'A full reload does not establish HMR compatibility')
        await callGo()
      } finally { page.off('framenavigated', onNavigate) }
    })
    else await check('reconnect', async () => {
      const previousCapability = (await getCapability()).value
      const previousConnections = ipcConnections
      await stopProcessGroup(child)
      await bounded(exit, 10_000, 'The stopped backend did not exit')
      let overlayFailure
      try { await page.locator('.wails-reconnect-overlay').waitFor({ state: 'visible', timeout: 5000 }) }
      catch (error) { overlayFailure = error }
      start()
      await waitForOwnedServer(`${baseURL}/${markerName}`, marker, child)
      const deadline = Date.now() + 20_000
      while (ipcConnections <= previousConnections && Date.now() < deadline) await delay(100)
      assert.ok(ipcConnections > previousConnections, 'The browser did not reconnect its IPC WebSocket')
      await callGo()
      await page.locator('.wails-reconnect-overlay').waitFor({ state: 'hidden' })
      assert.notEqual((await getCapability()).value, previousCapability, 'Restart must mint a new capability')
      result.checks.capabilityRotated = true
      if (overlayFailure) throw overlayFailure
    })
    await check('gracefulExit', async () => {
      await page.evaluate(() => window.runtime.Quit())
      assert.deepEqual(await bounded(exit, 20_000, 'Wails did not exit after runtime.Quit'), { code: 0, signal: null })
    })
  } catch (error) { result.failures.push({ stage, message: error.message, stack: error.stack }) }
  finally {
    if (tracker) { clearInterval(tracker); try { rememberDescendants(child, groups) } catch (error) { result.trackerError = error.message } }
    await check('browserClosed', async () => { if (context) await bounded(context.close(), 10_000, 'Browser cleanup timed out') })
    await check('processStopped', () => stopProcessGroup(child))
    if (mode === 'vite') await check('cliProcessesStopped', async () => {
      assert.equal(result.trackerError, undefined, result.trackerError)
      for (const pid of groups) await stopProcessGroup({ pid })
    })
    if (log !== undefined) closeSync(log)
    await check('listenerClosed', async () => { assert.ok(port); assert.equal(await portIsOpen(port), false, 'The Wails listener remained open') })
    if (mode === 'vite') await check('viteListenerClosed', async () => { assert.ok(vitePort); assert.equal(await portIsOpen(vitePort), false, 'The Vite listener remained open') })
    await check('isolatedDataRemoved', async () => { rmSync(directory, { recursive: true, force: true }); assert.equal(existsSync(directory), false) })
    process.removeListener('SIGINT', interrupt)
    process.removeListener('SIGTERM', interrupt)
    if (interrupted) result.failures.push({ stage: 'interrupted', message: 'Integration was interrupted' })
    result.passed = result.failures.length === 0 && browserErrors.length === 0
    writeFileSync(join(output, 'browser-errors.json'), JSON.stringify(browserErrors, null, 2) + '\n')
    writeFileSync(join(output, 'result.json'), JSON.stringify(result, null, 2) + '\n')
  }
  verifyEvidence(result, browserErrors, mode, process.env.GITHUB_SHA)
  console.log(`Wails/Echo ${mode} development integration passed`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runIntegration(process.argv[2], process.argv[3]).catch(error => { console.error(error); process.exitCode = 1 })
}
