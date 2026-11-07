import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { bounded, browserErrorDetails, privateAppEnvironment, requireCI, stopProcessGroup, verifyDevBuild, watchExit } from './wails-dev-integration.mjs'

const expected = { 'github.com/wailsapp/wails/v2': 'v2.90.0', 'github.com/labstack/echo/v4': 'v4.90.0' }
const info = `app: go1.27.3
\tdep\tgithub.com/wailsapp/wails/v2\tv2.90.0\th1:wails
\tdep\tgithub.com/labstack/echo/v4\tv4.90.0\th1:echo
\tbuild\t-tags=dev,desktop,webkit2_41,libsqlite3,sqlite_omit_load_extension
\tbuild\tCGO_ENABLED=1
\tbuild\tGOOS=linux
`

test('requires CI Linux and a virtual display before starting the real app', () => {
  assert.doesNotThrow(() => requireCI('linux', { GITHUB_ACTIONS: 'true', RUNNER_TEMP: '/tmp/ci', DISPLAY: ':99' }))
  assert.throws(() => requireCI('darwin', { GITHUB_ACTIONS: 'true' }), /CI Linux/)
  assert.throws(() => requireCI('linux', {}), /must not start locally/)
  assert.throws(() => requireCI('linux', { GITHUB_ACTIONS: 'true' }), /virtual display/)
})

test('the command refuses local execution before opening a GUI or listener', () => {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('./wails-dev-integration.mjs', import.meta.url))], {
    env: { ...process.env, GITHUB_ACTIONS: 'false' }, encoding: 'utf8', timeout: 5000,
  })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /CI Linux|must not start locally/)
})

test('requires the actual selected Echo and Wails in a development binary', () => {
  verifyDevBuild(info, expected)
  for (const changed of [
    info.replace('v4.90.0', 'v4.1.0'), info.replace('v2.90.0', 'v2.1.0'),
    info.split('\n').filter(line => !line.includes('echo/v4')).join('\n'),
    info + '\tdep\tgithub.com/labstack/echo/v4\tv4.90.0\th1:duplicate\n',
    info + '\t=>\t../local-echo\t(devel)\n', info.replace('dev,desktop', 'production,desktop'),
    info.replace('dev,desktop', 'dev,desktop,bindings'), info.replace('CGO_ENABLED=1', 'CGO_ENABLED=0'),
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
