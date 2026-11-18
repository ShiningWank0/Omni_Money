import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { mkdtempSync, rmSync, statSync } from 'node:fs'
import { createServer as createTLSServer } from 'node:https'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { bounded, browserErrorDetails, createDiagnosticTLS, probeHTTP, privateAppEnvironment, quitNativeApplication, requireCI, stopProcessGroup, verifyDevBuild, verifyEvidence, watchExit } from './wails-dev-integration.mjs'
import { verifyCLI } from './wails-dev-evidence.mjs'

const expected = { 'github.com/wailsapp/wails/v3': 'v3.90.0' }
const info = `app: go1.27.3
\tdep\tgithub.com/wailsapp/wails/v3\tv3.90.0\th1:wails
\tbuild\t-tags=dev,desktop,gtk3,libsqlite3,sqlite_omit_load_extension
\tbuild\tCGO_ENABLED=1
\tbuild\tGOOS=linux
\tbuild\tGOARCH=amd64
`

test('requires CI Linux and a virtual display before starting the real app', () => {
  assert.doesNotThrow(() => requireCI('linux', { GITHUB_ACTIONS: 'true', RUNNER_TEMP: '/tmp/ci', DISPLAY: ':99' }))
  assert.throws(() => requireCI('darwin', { GITHUB_ACTIONS: 'true' }), /CI Linux/)
  assert.throws(() => requireCI('linux', {}), /must not start locally/)
  assert.throws(() => requireCI('linux', { GITHUB_ACTIONS: 'true' }), /virtual display/)
})

test('development metadata checks all four Desktop platforms and rejects another CPU', () => {
  for (const platform of ['linux/amd64', 'darwin/amd64', 'darwin/arm64', 'windows/amd64']) {
    const [os, arch] = platform.split('/')
    const metadata = info.replace('GOOS=linux', `GOOS=${os}`).replace('GOARCH=amd64', `GOARCH=${arch}`)
    verifyDevBuild(metadata, expected, platform)
    assert.throws(() => verifyDevBuild(metadata.replace(`GOARCH=${arch}`, 'GOARCH=invalid'), expected, platform))
    assert.throws(() => verifyDevBuild(metadata.replace(`GOOS=${os}`, 'GOOS=invalid'), expected, platform))
  }
})

test('platform arguments cannot supply regular expressions or trailing path segments', () => {
  for (const platform of ['lin.x/amd64', 'linux/amd64|arm64', 'linux/amd64/extra', 'linux/amd64\n', 'linux/amd64$']) {
    assert.throws(() => verifyDevBuild(info, expected, platform), platform)
  }
  for (const setting of ['GOOS=linux', 'GOARCH=amd64']) {
    assert.throws(() => verifyDevBuild(info.replace(setting, `${setting}-extra`), expected))
    assert.throws(() => verifyDevBuild(info.replace(setting, 'unrelated=value'), expected))
  }
})

test('CLI rejects stale Wails 3, duplicates and replacements', () => {
  const cli = info.replace('dep\tgithub.com/wailsapp', 'mod\tgithub.com/wailsapp')
  verifyCLI(cli, expected)
  for (const changed of [cli.replace('v3.90.0', 'v3.0.0'), cli + '\n => ../local', cli + '\n mod github.com/wailsapp/wails/v3 v3.90.0']) {
    assert.throws(() => verifyCLI(changed, expected))
  }
})

test('compatibility evidence rejects omitted scenarios, cleanup failures, browser errors and a different commit', () => {
  for (const mode of ['assets', 'vite']) {
    const checks = Object.fromEntries(['http', 'goIPC', 'licenses', 'security', 'gracefulExit', 'browserClosed', 'processStopped', 'listenerClosed', 'isolatedDataRemoved',
      ...(mode === 'vite' ? ['viteProxy', 'hmr', 'cliBuild', 'viteListenerClosed', 'cliProcessesStopped'] : ['reconnect', 'capabilityRotated'])].map(key => [key, true]))
    const result = { mode, commit: 'checkout', passed: true, failures: [], checks, binarySHA256: 'a'.repeat(64), ipcRequests: { sent: 1, received: 1 } }
    verifyEvidence(result, [], mode, 'checkout')
    for (const key of Object.keys(checks)) {
      assert.throws(() => verifyEvidence({ ...result, checks: { ...checks, [key]: false } }, [], mode, 'checkout'), key)
      const missing = { ...checks }; delete missing[key]
      assert.throws(() => verifyEvidence({ ...result, checks: missing }, [], mode, 'checkout'), key)
    }
    assert.throws(() => verifyEvidence(result, [{ message: 'null.nodes' }], mode, 'checkout'))
    assert.throws(() => verifyEvidence(result, [], mode, 'another-checkout'))
    assert.throws(() => verifyEvidence({ ...result, passed: false }, [], mode, 'checkout'))
    assert.throws(() => verifyEvidence({ ...result, ipcRequests: { sent: 0, received: 0 } }, [], mode, 'checkout'))
  }
})

test('the command refuses local execution before opening a GUI or listener', () => {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('./wails-dev-integration.mjs', import.meta.url))], {
    env: { ...process.env, GITHUB_ACTIONS: 'false' }, encoding: 'utf8', timeout: 5000,
  })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /CI Linux|must not start locally/)
})

test('requires the selected Wails 3 and rejects native/headless tag confusion', () => {
  verifyDevBuild(info, expected)
  const browser = info.replace('dev,desktop', 'dev,desktop,server,omni_wails_browser')
  verifyDevBuild(browser, expected, 'linux/amd64', true)
  assert.throws(() => verifyDevBuild(info, expected, 'linux/amd64', true))
  for (const changed of [
    info.replace('v3.90.0', 'v3.0.0'),
    info.split('\n').filter(line => !line.includes('wails/v3')).join('\n'),
    info + '\tdep\tgithub.com/wailsapp/wails/v3\tv3.90.0\th1:duplicate\n',
    info + '\t=>\t../local-echo\t(devel)\n', info.replace('dev,desktop', 'production,desktop'),
    info.replace('dev,desktop', 'dev,desktop,bindings'), info.replace('dev,desktop', 'dev,desktop,purego'), info.replace('dev,desktop', 'dev,desktop,server,omni_wails_browser'), info.replace('CGO_ENABLED=1', 'CGO_ENABLED=0'),
  ]) assert.throws(() => verifyDevBuild(changed, expected))
})

test('isolates data and neutralizes Wails environment overrides without changing the parent', () => {
  const inherited = { devserver: 'other:123', assetdir: '/other', frontenddevserverurl: 'http://other', loglevel: 'debug', GITHUB_TOKEN: 'secret', sentinel: 'kept' }
  const env = privateAppEnvironment('/owned-test', inherited)
  for (const key of ['XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) assert.ok(env[key].startsWith('/owned-test/'))
  for (const key of ['devserver', 'assetdir', 'frontenddevserverurl', 'loglevel', 'GITHUB_TOKEN']) assert.equal(env[key], undefined)
  assert.equal(env.sentinel, 'kept')
  assert.equal(inherited.assetdir, '/other')
})

test('a missing response fails at its deadline', async () => {
  await assert.rejects(bounded(new Promise(() => {}), 10, 'response deadline'), /response deadline/)
})

function quitFixture(t, { response = 'closed', code = 0, stalled = false, signal = false } = {}) {
  // Only a disposable Node process; no native app, GUI or listener is started.
  const child = spawn(process.execPath, ['-e', `process.stdin.once('data', () => process.exit(${code})); setInterval(() => {}, 1000)`], { stdio: ['pipe', 'ignore', 'ignore'] })
  const exited = watchExit(child)
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill(); await exited })
  let closed = false
  const page = {
    isClosed: () => closed,
    evaluate: async () => {
      if (signal) child.kill()
      else if (!stalled) child.stdin.write('quit')
      if (response === 'hung') return new Promise(() => {})
      if (response === 'returned') return
      closed = response !== 'open'
      throw new Error(response === 'error' ? 'Application.Quit failed' : 'page.evaluate: Target page, context or browser has been closed')
    },
  }
  return { page, child, exited }
}

for (const response of ['returned', 'closed']) test(`native quit requires a real clean process exit when the request is ${response}`, async t => {
  const { page, child, exited } = quitFixture(t, { response })
  assert.deepEqual(await quitNativeApplication(page, child, exited, 5000), {
    code: 0, signal: null, quitResponse: response === 'returned' ? 'returned' : 'webview-closed',
  })
})

for (const setting of [{ code: 7 }, { signal: true }]) test(`native quit rejects abnormal process termination ${JSON.stringify(setting)}`, async t => {
  const { page, child, exited } = quitFixture(t, setting)
  await assert.rejects(quitNativeApplication(page, child, exited, 5000), /did not exit cleanly/)
})

for (const response of ['error', 'open']) test(`native quit preserves the request error when the response is ${response}`, async t => {
  const { page, child, exited } = quitFixture(t, { response })
  await assert.rejects(quitNativeApplication(page, child, exited, 5000), response === 'error' ? /Application.Quit failed/ : /Target page/)
})

test('native quit rejects an already exited app or already closed page before sending a request', async () => {
  const page = { isClosed: () => false, evaluate: () => assert.fail('Quit must not be sent') }
  await assert.rejects(quitNativeApplication(page, { exitCode: 0, signalCode: null }, Promise.resolve({ code: 0, signal: null })), /exited before/)
  await assert.rejects(quitNativeApplication(page, { exitCode: null, signalCode: 'SIGTERM' }, Promise.resolve()), /terminated before/)
  await assert.rejects(quitNativeApplication({ ...page, isClosed: () => true }, { exitCode: null, signalCode: null }, Promise.resolve()), /WebView closed before/)
})

test('native quit times out if the WebView closes but the owned process remains alive', async t => {
  const { page, child, exited } = quitFixture(t, { stalled: true })
  await assert.rejects(quitNativeApplication(page, child, exited, 100), /before the deadline/)
  assert.equal(child.exitCode, null)
  assert.equal(child.signalCode, null)
})

test('native quit times out if the process exits but the request never settles', async t => {
  const { page, child, exited } = quitFixture(t, { response: 'hung' })
  await assert.rejects(quitNativeApplication(page, child, exited, 100), /before the deadline/)
  assert.deepEqual(await bounded(exited, 5000, 'Fixture process did not exit'), { code: 0, signal: null })
})

test('browser diagnostics preserve the original source stack and stage', () => {
  const error = new TypeError("Cannot read properties of null (reading 'nodes')")
  error.stack = `${error.name}: ${error.message}\n    at runtime (http://127.0.0.1:1234/wails/ipc.js:1:42)`
  assert.deepEqual(browserErrorDetails(error, 'loading application'), {
    stage: 'loading application', name: 'TypeError', message: error.message, stack: error.stack,
  })
})

for (const ignoreTerm of [false, true]) test(`cleanup stops the owned process group (ignore SIGTERM: ${ignoreTerm})`, async t => {
  // Only a disposable Node child; no GUI, network listener, or real app.
  const program = `${ignoreTerm ? "process.on('SIGTERM', () => {});" : ''} process.stdout.write('ready'); setInterval(() => {}, 1000);`
  const child = spawn(process.execPath, ['-e', program], { detached: true, stdio: ['ignore', 'pipe', 'ignore'] })
  const exit = watchExit(child)
  t.after(() => stopProcessGroup(child))
  await bounded(once(child.stdout, 'data'), 5000, 'fixture startup timeout')
  await stopProcessGroup(child)
  const result = await bounded(exit, 5000, 'fixture cleanup timeout')
  assert.equal(result.signal, ignoreTerm ? 'SIGKILL' : 'SIGTERM')
})

test('startup failure is observed and cleanup does not target another process', async () => {
  const missing = fileURLToPath(new URL('./__missing_wails_dev_fixture__', import.meta.url))
  const child = spawn(missing, [], { detached: true, stdio: 'ignore' })
  await assert.rejects(watchExit(child), /ENOENT/)
  await stopProcessGroup(child)
})

test('diagnostic HTTPS probes trust only the run certificate and verify its hostname', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'omni-tls-fixture-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const tls = createDiagnosticTLS(directory)
  assert.equal(statSync(join(directory, 'loopback-key.pem')).mode & 0o777, 0o600)
  const server = createTLSServer({ cert: tls.certificate, key: tls.privateKey, minVersion: 'TLSv1.3' }, (_, response) => response.end('owned fixture'))
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => server.close(resolve)))
  const baseURL = `https://127.0.0.1:${server.address().port}`
  await assert.rejects(probeHTTP(baseURL, '/'), /self.signed certificate/i)
  const response = await probeHTTP(baseURL, '/', {}, false, 'GET', { ca: tls.certificate })
  assert.equal(response.status, 200)
  assert.equal(response.body, 'owned fixture')
  await assert.rejects(probeHTTP(baseURL, '/', {}, false, 'GET', { ca: tls.certificate, servername: 'other.invalid' }), { code: 'ERR_TLS_CERT_ALTNAME_INVALID' })
})

test('cleanup stops and reaps descendants after their parent exits', async t => {
  // The native WebView also creates children that can outlive its main process.
  // A container needs an init reaper; merely sending SIGKILL leaves zombies.
  const descendant = "process.stdout.write('ready'); setInterval(() => {}, 1000)"
  const program = `require('node:child_process').spawn(process.execPath, ['-e', ${JSON.stringify(descendant)}], { stdio: ['ignore', 1, 2] }); process.exit(0)`
  const child = spawn(process.execPath, ['-e', program], { detached: true, stdio: ['ignore', 'pipe', 'ignore'] })
  const exit = watchExit(child)
  t.after(() => stopProcessGroup(child))
  await bounded(once(child.stdout, 'data'), 5000, 'descendant startup timeout')
  assert.deepEqual(await bounded(exit, 5000, 'parent exit timeout'), { code: 0, signal: null })
  await stopProcessGroup(child)
  assert.throws(() => process.kill(-child.pid, 0), { code: 'ESRCH' })
})
