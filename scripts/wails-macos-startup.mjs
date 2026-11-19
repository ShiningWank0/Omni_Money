import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { bounded, privateAppEnvironment, stopProcessGroup, watchExit } from './wails-dev-integration.mjs'
import { ownedBinary } from './wails-native-integration.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))

export function requireMacCI(platform, env) {
  assert.equal(platform, 'darwin', 'macOS native startup only')
  assert.equal(env.GITHUB_ACTIONS, 'true', 'CI only; never launch against a developer profile')
  assert.ok(env.RUNNER_TEMP && env.DEVELOPER_DIR, 'The CI runner and fixed Xcode are required')
}

export function hasOwnedWindow(rows, pid) {
  assert.ok(Array.isArray(rows), 'Invalid native window inventory')
  assert.ok(rows.every(row => row.pid === pid && Number.isInteger(row.layer) &&
    Number.isFinite(row.width) && Number.isFinite(row.height)), 'Invalid owned window metadata')
  return rows.some(row => row.layer === 0 && row.width > 0 && row.height > 0)
}

export function verifyMacBuild(info, version, arch) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced native dependencies are not allowed')
  assert.equal([...info.matchAll(/^\s*dep\s+github\.com\/wailsapp\/wails\/v3\s+(\S+)/gm)].map(x => x[1]).join(','), version)
  const tags = new Set((info.match(/^\s*build\s+-tags=(.+)$/m)?.[1] || '').replace(/^"|"$/g, '').split(','))
  for (const tag of ['production', 'desktop', 'libsqlite3', 'sqlite_omit_load_extension']) assert.ok(tags.has(tag), `Missing ${tag} build tag`)
  for (const tag of ['dev', 'bindings', 'server', 'omni_wails_browser', 'purego']) assert.ok(!tags.has(tag), `Wrong ${tag} build tag`)
  for (const setting of ['CGO_ENABLED=1', 'GOOS=darwin', `GOARCH=${arch}`]) {
    assert.ok(info.split('\n').some(line => line.trim().replace(/^build\s+/, '') === setting), `Missing ${setting}`)
  }
}

export async function runMacStartup(argument) {
  requireMacCI(process.platform, process.env)
  const binary = ownedBinary(argument, process.env.RUNNER_TEMP)
  const command = (program, args, env = process.env) => {
    const result = spawnSync(program, args, { env, cwd: root, encoding: 'utf8', timeout: 60_000, maxBuffer: 2 * 1024 * 1024 })
    assert.equal(result.status, 0, result.stderr || result.error?.message || `${program} failed`)
    return result.stdout
  }
  const version = command('go', ['list', '-mod=readonly', '-m', '-f', '{{.Version}}{{if .Replace}} replaced{{end}}', 'github.com/wailsapp/wails/v3']).trim()
  assert.match(version, /^v3\.\d+\.\d+$/)
  const info = command('go', ['version', '-m', binary])
  verifyMacBuild(info, version, process.arch === 'x64' ? 'amd64' : process.arch)
  const digest = () => createHash('sha256').update(readFileSync(binary)).digest('hex')
  const before = digest()
  const directory = mkdtempSync(join(process.env.RUNNER_TEMP, 'omni-wails-macos-'))
  const output = join(root, '.oss-cache/wails-macos-results')
  mkdirSync(output, { recursive: true, mode: 0o700 })
  const env = privateAppEnvironment(directory, process.env)
  env.HOME = join(directory, 'home')
  for (const key of ['HOME', 'XDG_DATA_HOME', 'XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR', 'TMPDIR']) mkdirSync(env[key], { recursive: true, mode: 0o700 })
  let app, failure, windowCreated = false, stable = false, findingWindow = true
  const log = openSync(join(output, 'startup.log'), 'w', 0o600)
  try {
    const inspector = join(directory, 'inspect-owned-windows')
    command('xcrun', ['swiftc', join(root, 'scripts/wails-macos-windows.swift'), '-o', inspector])
    app = spawn(binary, [], { cwd: directory, env, detached: true, stdio: ['ignore', log, log] })
    const exited = watchExit(app)
    exited.catch(() => {})
    const stopped = exited.then(result => { throw new Error(`Native app exited during startup: ${JSON.stringify(result)}`) })
    stopped.catch(() => {})
    await bounded(Promise.race([stopped, (async () => {
      while (findingWindow && app.exitCode === null && app.signalCode === null) {
        if (hasOwnedWindow(JSON.parse(command(inspector, [String(app.pid)])), app.pid)) return
        await delay(250)
      }
      throw new Error('Native window discovery stopped before a window was created')
    })()]), 45_000, 'The packaged macOS app did not create an owned native window')
    windowCreated = true
    await Promise.race([stopped, delay(5000)])
    stable = true
    assert.doesNotMatch(readFileSync(join(output, 'startup.log'), 'utf8'), /panic:|fatal error:|Terminating app due to uncaught exception/)
    assert.equal(digest(), before, 'The tested distribution binary changed')
  } catch (error) { failure = error }
  finally {
    findingWindow = false
    try { await stopProcessGroup(app) } catch (error) { failure ||= error }
    closeSync(log)
    try { rmSync(directory, { recursive: true, force: true }) } catch (error) { failure ||= error }
    writeFileSync(join(output, 'result.json'), JSON.stringify({ passed: !failure, wails: version, binarySHA256: before,
      windowCreated, stableAfterWindowCreation: stable, isolatedDataRemoved: !existsSync(directory),
      coverage: 'native startup smoke; full macOS UI interaction is not covered', error: failure?.message || null }, null, 2) + '\n')
  }
  if (failure) throw failure
  console.log('Packaged macOS app created a native window and survived startup without a crash')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runMacStartup(process.argv[2]).catch(error => { console.error(error); process.exitCode = 1 })
}
