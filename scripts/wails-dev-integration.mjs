import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { chmodSync, closeSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { request } from 'node:http'
import { request as requestTLS } from 'node:https'
import { createHash, X509Certificate } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { createServer, createConnection, isIP } from 'node:net'
import { dirname, isAbsolute, join, relative, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const modulePaths = ['github.com/wailsapp/wails/v3']

export function requireCI(platform, env) {
  assert.equal(platform, 'linux', 'The real Wails integration runs only on CI Linux')
  assert.equal(env.GITHUB_ACTIONS, 'true', 'The real Wails integration must not start locally')
  assert.ok(env.RUNNER_TEMP && env.DISPLAY, 'RUNNER_TEMP and the virtual display are required')
}

export function verifyDevBuild(info, expected, platform = 'linux/amd64', browser = false) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced dependencies are not allowed')
  const dependencies = [...info.matchAll(/^\s*dep\s+(\S+)\s+(\S+)/gm)]
  assert.equal(new Set(dependencies.map(match => match[1])).size, dependencies.length, 'Duplicate dependency')
  for (const path of modulePaths) {
    assert.ok(expected[path], `Missing selected version for ${path}`)
    assert.equal(dependencies.find(match => match[1] === path)?.[2], expected[path], `Built ${path} differs from the project graph`)
  }
  const tags = new Set((info.match(/^\s*build\s+-tags=(.+)$/m)?.[1] || '').replace(/^"|"$/g, '').split(','))
  const [goos, goarch] = platform.split('/')
  assert.equal(platform, `${goos}/${goarch}`, 'Expected exactly OS/CPU for the development build platform')
  assert.ok(['linux', 'darwin', 'windows'].includes(goos) && ['amd64', 'arm64'].includes(goarch), 'Unsupported development build platform')
  for (const tag of ['dev', 'desktop', 'libsqlite3', 'sqlite_omit_load_extension', ...(goos === 'linux' ? ['gtk3'] : [])]) assert.ok(tags.has(tag), `Missing integration build tag: ${tag}`)
  for (const tag of ['production', 'bindings', 'purego', ...(browser ? [] : ['server', 'omni_wails_browser'])]) assert.ok(!tags.has(tag), `Wrong integration build tag: ${tag}`)
  if (browser) for (const tag of ['server', 'omni_wails_browser']) assert.ok(tags.has(tag), 'Missing diagnostic host tag: ' + tag)
  assert.match(info, /^\s*build\s+CGO_ENABLED=1$/m)
  assert.equal(info.match(/^\s*build\s+GOOS=(\S+)$/m)?.[1], goos, 'Built operating system differs from the requested platform')
  assert.equal(info.match(/^\s*build\s+GOARCH=(\S+)$/m)?.[1], goarch, 'Built CPU differs from the requested platform')
}

export function privateAppEnvironment(directory, inherited) {
  const env = { ...inherited, XDG_DATA_HOME: join(directory, 'data'), XDG_CONFIG_HOME: join(directory, 'config'),
    XDG_CACHE_HOME: join(directory, 'cache'), XDG_RUNTIME_DIR: join(directory, 'runtime'), TMPDIR: join(directory, 'tmp'),
    GDK_BACKEND: 'x11', WEBKIT_DISABLE_COMPOSITING_MODE: '1', LIBGL_ALWAYS_SOFTWARE: '1' }
  // These upstream Wails environment variables take precedence over CLI flags.
  for (const key of ['devserver', 'assetdir', 'frontenddevserverurl', 'loglevel', 'GITHUB_TOKEN', 'GH_TOKEN', 'WAILS_DEV_ALLOWED_HOSTS', 'FRONTEND_DEVSERVER_URL', 'WAILS_SERVER_HOST', 'WAILS_SERVER_PORT', 'OMNI_WAILS_BROWSER_PORT', 'OMNI_WAILS_VITE_URL', 'OMNI_WAILS_ASSET_DIR', 'OMNI_WAILS_TLS_CERT', 'OMNI_WAILS_TLS_KEY']) delete env[key]
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

export async function quitNativeApplication(page, child, exited, milliseconds = 15_000) {
  assert.equal(child.exitCode, null, 'Native application exited before the quit request')
  assert.equal(child.signalCode, null, 'Native application was terminated before the quit request')
  assert.equal(page.isClosed(), false, 'Native WebView closed before the quit request')
  const request = page.evaluate(async () => (await import('/wails/runtime.js')).Application.Quit()).then(
    () => 'returned',
    error => {
      // Quit destroys the native WebView, so CDP may close before replying.
      // Accept only that specific transport closure, and only if the page
      // actually closed. The owned process must still exit cleanly below.
      if (error.name !== 'Error' || !error.message.startsWith('page.evaluate: Target page, context or browser has been closed') || !page.isClosed()) throw error
      return 'webview-closed'
    },
  )
  const [quitResponse, result] = await bounded(Promise.all([request, exited]), milliseconds, 'Native application did not complete shutdown before the deadline')
  assert.deepEqual(result, { code: 0, signal: null }, 'Native application did not exit cleanly')
  return { ...result, quitResponse }
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

async function waitForOwnedServer(url, marker, child, timeout = 45_000, tlsOptions = {}) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    assert.equal(child.exitCode, null, 'Wails exited before serving the application')
    assert.equal(child.signalCode, null, 'Wails was terminated before serving the application')
    try {
      const response = await probeHTTP(url, '', {}, false, 'GET', tlsOptions)
      if (response.status === 200 && response.body === marker) return
    } catch { /* The owned listener may not be ready yet. */ }
    await delay(200)
  }
  throw new Error('Wails 3 did not serve this test instance before the deadline')
}

function go(args, cwd = root) {
  const result = spawnSync('go', args, { cwd, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
  assert.equal(result.status, 0, result.stderr || String(result.error || 'Go inspection failed'))
  return result.stdout
}

// A real HTTP upgrade, without a browser's automatically attached cookies.
export function probeHTTP(baseURL, path, headers = {}, upgrade = false, method = 'GET', tlsOptions = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, baseURL)
    const https = url.protocol === 'https:'
    // Authenticate the URL host even when the probe sets a different HTTP Host.
    const servername = isIP(url.hostname) ? '' : url.hostname
    const transport = https ? { servername, ...tlsOptions } : {}
    const req = (https ? requestTLS : request)(url, { ...transport, method, headers: { ...headers, ...(upgrade ? {
      Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13',
      'Sec-WebSocket-Key': Buffer.from(randomUUID().replaceAll('-', '').slice(0, 16)).toString('base64'),
    } : {}) } })
    req.setTimeout(5000, () => req.destroy(new Error('HTTP probe timed out')))
    req.once('error', reject)
    req.once('upgrade', (response, socket) => { socket.destroy(); resolve({ status: response.statusCode, headers: response.headers }) })
    req.once('response', response => {
      let body = ''
      response.setEncoding('utf8')
      response.on('data', chunk => { body += chunk; if (body.length > 1 << 20) req.destroy(new Error('HTTP probe response exceeds limit')) })
      response.once('error', reject)
      response.once('end', () => resolve({ status: response.statusCode, headers: response.headers, body }))
    })
    req.end()
  })
}

export function createDiagnosticTLS(directory) {
  const cert = join(directory, 'loopback-cert.pem'), key = join(directory, 'loopback-key.pem')
  // In real CI this is the OpenSSL binary in the verified Linux snapshot.
  const generated = spawnSync('openssl', ['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1',
    '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1'],
  { encoding: 'utf8', timeout: 10_000 })
  assert.equal(generated.status, 0, generated.stderr || generated.error?.message)
  chmodSync(key, 0o600)
  const certificate = readFileSync(cert, 'utf8'), privateKey = readFileSync(key, 'utf8')
  const spki = new X509Certificate(certificate).publicKey.export({ type: 'spki', format: 'der' })
  return { certificate, privateKey, fingerprint: createHash('sha256').update(spki).digest('base64') }
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
  assert.ok(result.ipcRequests.sent > 0 && result.ipcRequests.received > 0)
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
  verifyDevBuild(info, expected, 'linux/amd64', true)
  const output = join(root, '.oss-cache/wails-dev-results')
  mkdirSync(output, { recursive: true, mode: 0o700 })
  writeFileSync(join(output, 'build-info.txt'), info)
  const directory = mkdtempSync(join(runnerTemp, 'omni-wails-dev-'))
  const result = { mode, commit: process.env.GITHUB_SHA, modules: expected, node: process.version,
    binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex'),
    checks: {}, failures: [], ipcRequests: { sent: 0, received: 0 } }
  const browserErrors = [], groups = new Set()
  let child, exit, context, port, backendPort, vitePort, viteChild, log, tracker, interrupted = false, stage = 'starting'
  const check = async (name, callback) => {
    stage = name
    try { await callback(); result.checks[name] = true }
    catch (error) { result.failures.push({ stage, message: error.message, stack: error.stack }); result.checks[name] = false }
  }
  const interrupt = () => { interrupted = true; void context?.close().catch(() => {}); for (const owned of [child, viteChild]) void stopProcessGroup(owned).catch(() => {}) }
  process.once('SIGINT', interrupt)
  process.once('SIGTERM', interrupt)
  try {
    const env = privateAppEnvironment(directory, process.env)
    for (const key of ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) mkdirSync(env[key], { recursive: true, mode: 0o700 })
    port = await unusedPort()
    const baseURL = `https://127.0.0.1:${port}`
    const tls = createDiagnosticTLS(directory), trust = { ca: tls.certificate }
    const probe = (url, path, headers = {}, upgrade = false, method = 'GET') => probeHTTP(url, path, headers, upgrade, method, trust)
    const marker = randomUUID(), markerName = `integration-${marker}.txt`
    let project = root, assets = join(directory, 'assets'), appSource
    let executable = binary, args = []
    do { backendPort = await unusedPort() } while (backendPort === port)
    env.WAILS_SERVER_HOST = '127.0.0.1'; env.WAILS_SERVER_PORT = String(backendPort); env.OMNI_WAILS_BROWSER_PORT = String(port)
    if (mode === 'vite') {
      project = join(directory, 'project')
      cpSync(root, project, { recursive: true, filter: source => {
        const parts = relative(root, source).split('/')
        return !parts.some(part => ['.git', '.oss-cache', 'node_modules', '.codex', '.agents', '.aws'].includes(part) || part.startsWith('.env')) &&
          !relative(root, source).startsWith('build/bin')
      } })
      // Keep relative file: dependency links inside the private checkout.
      cpSync(join(root, 'frontend/node_modules'), join(project, 'frontend/node_modules'), { recursive: true, verbatimSymlinks: true })
      do { vitePort = await unusedPort() } while ([port, backendPort].includes(vitePort))
      appSource = join(project, 'frontend/src/App.vue')
      const vue = readFileSync(appSource, 'utf8')
      assert.ok(vue.includes('<div id="app">'), 'Expected the real application root')
      writeFileSync(appSource, vue.replace('<div id="app">', `<div id="app"><span data-ci-hmr>${marker}</span>`))
      assets = join(project, 'frontend/dist')
      writeFileSync(join(project, 'frontend', markerName), marker)
      const cli = join(go(['env', 'GOPATH']).trim(), 'bin/wails3')
      executable = join(project, 'build/bin/omni-wails-browser')
      env.OMNI_CI_BROWSER_BINARY = executable
      const built = spawnSync(cli, ['task', 'ci-browser-build'], { cwd: project, env, encoding: 'utf8', timeout: 240_000 })
      assert.equal(built.status, 0, built.stderr || built.error?.message)
      const builtInfo = go(['version', '-m', executable], project)
      verifyDevBuild(builtInfo, expected, 'linux/amd64', true)
      writeFileSync(join(output, 'cli-build-info.txt'), builtInfo)
      result.cliBinarySHA256 = createHash('sha256').update(readFileSync(executable)).digest('hex')
      result.checks.cliBuild = true
      env.WAILS_VITE_PORT = String(vitePort)
      env.OMNI_WAILS_VITE_URL = `http://127.0.0.1:${vitePort}`
      result.frontendWatcher = 'node build/desktop.mjs dev'
    } else {
      cpSync(join(root, 'frontend/dist'), assets, { recursive: true })
      writeFileSync(join(assets, markerName), marker)
      env.OMNI_WAILS_ASSET_DIR = assets
    }
    log = openSync(join(output, 'application.log'), 'w', 0o600)
    const start = () => {
      assert.equal(interrupted, false, 'Integration was interrupted')
      child = spawn(executable, args, { cwd: project,
        env: { ...env, OMNI_WAILS_TLS_CERT: tls.certificate, OMNI_WAILS_TLS_KEY: tls.privateKey },
        detached: true, stdio: ['ignore', log, log] })
      exit = watchExit(child); exit.catch(() => {})
    }
    if (mode === 'vite') {
      viteChild = spawn(process.execPath, ['build/desktop.mjs', 'dev'], { cwd: join(project, 'frontend'), env, detached: true, stdio: ['ignore', log, log] })
      watchExit(viteChild).catch(() => {})
      await waitForOwnedServer(`${env.OMNI_WAILS_VITE_URL}/${markerName}`, marker, viteChild)
    }
    start()
    if (mode === 'vite') tracker = setInterval(() => { try { rememberDescendants(child, groups) } catch (error) { result.trackerError = error.message } }, 300)
    await waitForOwnedServer(`${baseURL}/${markerName}`, marker, child, mode === 'vite' ? 240_000 : 45_000, trust)
    result.checks.http = true
    if (mode === 'vite') {
      result.checks.viteProxy = true
      for (const manifest of ['go.mod', 'go.sum']) assert.equal(readFileSync(join(project, manifest), 'utf8'), readFileSync(join(root, manifest), 'utf8'), `Wails task changed ${manifest}`)
    }
    const { chromium } = createRequire(join(root, 'frontend/package.json'))('@playwright/test')
    const browserEnv = { ...env }
    delete browserEnv.OMNI_WAILS_TLS_KEY
    context = await chromium.launchPersistentContext(join(directory, 'browser-profile'), { headless: true, locale: 'ja-JP', timeout: 30_000,
      env: browserEnv, args: [`--ignore-certificate-errors-spki-list=${tls.fingerprint}`] })
    const page = await context.newPage()
    page.setDefaultTimeout(15_000)
    page.on('pageerror', error => browserErrors.push(browserErrorDetails(error, stage)))
    page.on('request', req => { if (req.url() === baseURL + '/wails/runtime' && req.method() === 'POST') result.ipcRequests.sent++ })
    page.on('response', response => { if (response.url() === baseURL + '/wails/runtime' && response.status() === 200) result.ipcRequests.received++ })
    stage = 'loading application'
    const response = await page.goto(baseURL, { waitUntil: 'domcontentloaded', timeout: 30_000 })
    assert.equal(response.status(), 200)
    await page.waitForFunction(() => typeof window.go?.main?.App?.GetDesktopVaultStatus === 'function')
    const callGo = async () => {
      const status = await bounded(page.evaluate(() => window.go.main.App.GetDesktopVaultStatus()), 15_000, 'The real Go binding did not reply')
      assert.deepEqual(status, { configured: false, unlocked: false, legacy_migration_required: false, role: '' })
      assert.ok(result.ipcRequests.sent > 0 && result.ipcRequests.received > 0)
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
      assert.ok(cookie?.value && cookie.secure && cookie.httpOnly && cookie.sameSite === 'Strict', 'Expected a protected capability cookie')
      return cookie
    }
    await check('security', async () => {
      const cookie = await getCapability(), valid = `${cookie.name}=${cookie.value}`
      const deniedHost = await probe(baseURL, '/', { Host: `attacker.invalid:${port}` })
      assert.equal(deniedHost.status, 403)
      assert.equal(deniedHost.headers['set-cookie'], undefined)
      for (const headers of [
        { Origin: baseURL }, // missing capability
        { Origin: baseURL, Cookie: `${cookie.name}=invalid` },
        { Origin: 'http://attacker.invalid', Cookie: valid },
        { Cookie: valid }, // missing Origin
        { Host: `attacker.invalid:${port}`, Origin: `http://attacker.invalid:${port}`, Cookie: valid },
      ]) assert.equal((await probe(baseURL, '/wails/events', headers, true)).status, 403)
      for (const headers of [{ Origin: baseURL }, { Origin: 'http://attacker.invalid', Cookie: valid }, { Cookie: valid }]) {
        assert.equal((await probe(baseURL, '/wails/runtime', headers, false, 'POST')).status, 403)
      }
      assert.equal((await probe(`http://127.0.0.1:${backendPort}`, '/wails/runtime', { Origin: baseURL }, false, 'POST')).status, 403)
      assert.equal((await probe(baseURL, '/wails/events', { Origin: baseURL, Cookie: valid }, true)).status, 101)
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
      await stopProcessGroup(child)
      await bounded(exit, 10_000, 'The stopped backend did not exit')
      start()
      await waitForOwnedServer(`${baseURL}/${markerName}`, marker, child, 45_000, trust)
      const oldCookie = `wails_ipc_capability_${port}=${previousCapability}`
      assert.equal((await probe(baseURL, '/wails/events', { Origin: baseURL, Cookie: oldCookie }, true)).status, 403)
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.waitForFunction(() => typeof window.go?.main?.App?.GetDesktopVaultStatus === 'function')
      await callGo()
      assert.notEqual((await getCapability()).value, previousCapability, 'Restart must mint a new capability')
      result.checks.capabilityRotated = true
    })
    await check('gracefulExit', async () => {
      await page.evaluate(async () => (await import('/wails/runtime.js')).Application.Quit())
      assert.deepEqual(await bounded(exit, 20_000, 'Wails did not exit after runtime.Quit'), { code: 0, signal: null })
    })
  } catch (error) { result.failures.push({ stage, message: error.message, stack: error.stack }) }
  finally {
    if (tracker) { clearInterval(tracker); try { rememberDescendants(child, groups) } catch (error) { result.trackerError = error.message } }
    await check('browserClosed', async () => { if (context) await bounded(context.close(), 10_000, 'Browser cleanup timed out') })
    await check('processStopped', () => stopProcessGroup(child))
    if (mode === 'vite') await check('cliProcessesStopped', async () => {
      for (const pid of groups) await stopProcessGroup({ pid })
      await stopProcessGroup(viteChild)
      assert.equal(result.trackerError, undefined, result.trackerError)
    })
    if (log !== undefined) closeSync(log)
    await check('listenerClosed', async () => { for (const value of [port, backendPort]) { assert.ok(value); assert.equal(await portIsOpen(value), false, 'A Wails listener remained open') } })
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
  console.log(`Wails 3 ${mode} development integration passed`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runIntegration(process.argv[2], process.argv[3]).catch(error => { console.error(error); process.exitCode = 1 })
}
