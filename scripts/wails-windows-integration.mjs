import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { createServer } from 'node:net'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { bounded, watchExit } from './wails-dev-integration.mjs'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
assert.equal(process.env.GITHUB_ACTIONS, 'true', 'CI only')
assert.equal(process.platform, 'win32', 'Windows native WebView only')
const runtime = process.env.OMNI_CI_WEBVIEW2_RUNTIME
assert.ok(runtime && resolve(runtime).startsWith(resolve(process.env.RUNNER_TEMP)))
const lock = JSON.parse(readFileSync(join(root, 'build/locks/webview2.json'), 'utf8'))
const { chromium } = await import(pathToFileURL(join(root, 'frontend/node_modules/playwright-core/index.mjs')))
const binary = resolve(process.argv[2])
assert.equal(binary, join(root, 'build/bin/omni_money.exe'), 'Test the actual distribution binary')
const digest = () => createHash('sha256').update(readFileSync(binary)).digest('hex')
const before = digest()
const directory = mkdtempSync(join(process.env.RUNNER_TEMP, 'omni-wails-windows-'))
const output = join(root, '.oss-cache/wails-windows-results')
mkdirSync(output, { recursive: true })
const password = `windows-ci-${randomUUID()}`
const env = { ...process.env, APPDATA: join(directory, 'roaming'), LOCALAPPDATA: join(directory, 'local'),
  USERPROFILE: directory, HOME: directory, TEMP: join(directory, 'tmp'), TMP: join(directory, 'tmp'),
  OMNI_CI_WEBVIEW2_PROFILE: join(directory, 'webview-profile') }
for (const key of ['APPDATA', 'LOCALAPPDATA', 'TEMP']) mkdirSync(env[key], { recursive: true })
const rounds = []
let app, browser, failed = false
try {
  for (const scenario of ['setup', 'reopen']) {
    const server = createServer()
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
    const port = server.address().port
    await new Promise(resolve => server.close(resolve))
    // Wails' Go loader deliberately clears WEBVIEW2_* environment overrides.
    // The native WebView2 command-line switch survives that boundary.
    app = spawn(binary, [`--edge-webview-switches=--remote-debugging-port=${port} --remote-debugging-address=127.0.0.1`],
      { cwd: directory, env, stdio: ['ignore', 'pipe', 'pipe'] })
    let startupOutput = ''
    for (const stream of [app.stdout, app.stderr]) stream.on('data', data => { startupOutput = (startupOutput + data).slice(-8192) })
    const exited = watchExit(app)
    exited.catch(() => {})
    browser = await bounded((async () => {
      while (app.exitCode === null && app.signalCode === null) {
        try { return await chromium.connectOverCDP(`http://127.0.0.1:${port}`, { timeout: 1000 }) }
        catch { await new Promise(resolve => setTimeout(resolve, 200)) }
      }
      throw Error('Native app exited before creating its WebView: ' + startupOutput)
    })(), 45_000, 'Native WebView did not become ready')
    const version = await (await browser.newBrowserCDPSession()).send('Browser.getVersion')
    assert.equal(version.product.split('/').at(-1), lock.version, 'Actual WebView version differs from lock')
    const processes = spawnSync('pwsh', ['-NoProfile', '-Command',
      `Get-CimInstance Win32_Process -Filter 'ParentProcessId=${app.pid}' | Where-Object { $_.Name -eq 'msedgewebview2.exe' } | Select-Object -ExpandProperty ExecutablePath | ConvertTo-Json -Compress`], { encoding: 'utf8' })
    assert.equal(processes.status, 0, processes.stderr)
    const paths = [JSON.parse(processes.stdout)].flat()
    assert.ok(paths.length && paths.every(path => path.toLowerCase().startsWith(resolve(runtime).toLowerCase() + String.fromCharCode(92))),
      'Actual WebView process did not load the fixed runtime directory')
    const page = browser.contexts()[0].pages()[0]
    page.setDefaultTimeout(20_000)
    await page.locator('.desktop-vault-gate').waitFor({ state: 'visible' })
    await page.getByRole('button', { name: 'オープンソースライセンス', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'visible' })
    await page.getByRole('button', { name: 'ライセンスを閉じる', exact: true }).click()
    if (scenario === 'setup') {
      await page.locator('#desktop-new-password').fill(password)
      await page.locator('#desktop-new-password-confirmation').fill(password)
      await page.getByRole('button', { name: '管理者と保管庫を作成', exact: true }).click()
      await page.locator('#desktop-recovery-output').waitFor()
      assert.ok((await page.locator('#desktop-recovery-output').inputValue()).length > 10)
      await page.locator('.save-confirmation input').check()
      await page.getByRole('button', { name: '保管庫を開く', exact: true }).click()
    } else {
      await page.locator('#desktop-password').fill(password)
      await page.getByRole('button', { name: 'ロックを解除', exact: true }).click()
    }
    await page.locator('.desktop-vault-gate').waitFor({ state: 'detached' })
    const status = await page.evaluate(() => window.go.main.App.GetDesktopVaultStatus())
    assert.equal(status.unlocked, true, 'Real native binding did not unlock the SQLCipher vault')
    await page.evaluate(async () => (await import('/wails/runtime.js')).Application.Quit())
    const result = await bounded(exited, 15_000, 'Native app did not shut down')
    assert.equal(result.code, 0)
    await browser.close().catch(() => {})
    browser = null; app = null
    rounds.push({ scenario, runtimeVersion: lock.version, fixedRuntimeProcess: true, vaultUnlocked: true, gracefulExit: true })
  }
  assert.equal(digest(), before, 'Distribution binary changed during the test')
} catch (error) { failed = true; throw error }
finally {
  await browser?.close().catch(() => {})
  if (app?.pid && app.exitCode === null) spawnSync('taskkill', ['/PID', String(app.pid), '/T', '/F'])
  rmSync(directory, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 })
  writeFileSync(join(output, 'result.json'), JSON.stringify({ passed: !failed, binarySHA256: before,
    runtime: lock.version, rounds, privateProfileRemoved: true }, null, 2) + '\n')
}
