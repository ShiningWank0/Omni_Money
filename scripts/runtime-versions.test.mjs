import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { checkRuntimeVersions, syncRuntimeVersions } from './runtime-versions.mjs'

function fixture(t, { go = '1.26.7', node = '24.19.0', digest = 'a'.repeat(64) } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'omni-runtime-versions-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  writeFileSync(join(root, 'go.mod'), 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.7\n')
  writeFileSync(join(root, '.node-version'), '24.19.0\n')
  writeFileSync(join(root, 'Dockerfile'),
    `FROM node:${node}-alpine@sha256:${digest} AS frontend-builder\nFROM golang:${go}-alpine@sha256:${digest} AS backend-builder\n`)
  return root
}

test('aligned pins pass without rewriting files', t => {
  const root = fixture(t)
  const before = readFileSync(join(root, 'go.mod'), 'utf8')
  assert.equal(checkRuntimeVersions(root).toolchain, '1.26.7')
  syncRuntimeVersions(root)
  assert.equal(readFileSync(join(root, 'go.mod'), 'utf8'), before)
})

test('a Docker-only update fails until both pins are synchronized', t => {
  const root = fixture(t, { go: '1.27.1', node: '26.10.0' })
  assert.throws(() => checkRuntimeVersions(root), /Go toolchain.*Node version file.*mismatch/)
  syncRuntimeVersions(root)
  assert.equal(checkRuntimeVersions(root).toolchain, '1.27.1')
  assert.equal(readFileSync(join(root, '.node-version'), 'utf8'), '26.10.0\n')
  assert.match(readFileSync(join(root, 'go.mod'), 'utf8'), /^go 1\.26\.6$/m)
})

test('rejects downgrades below minimum Go without modifying either file', t => {
  const root = fixture(t, { go: '1.26.5', node: '26.10.0' })
  assert.throws(() => syncRuntimeVersions(root), /older than go.mod minimum/)
  assert.match(readFileSync(join(root, 'go.mod'), 'utf8'), /toolchain go1\.26\.7/)
  assert.equal(readFileSync(join(root, '.node-version'), 'utf8'), '24.19.0\n')
})

test('rejects missing digests and ambiguous builder stages', t => {
  const root = fixture(t, { digest: 'not-a-digest' })
  assert.throws(() => syncRuntimeVersions(root), /digest-pinned/)
  const valid = fixture(t)
  const path = join(valid, 'Dockerfile')
  writeFileSync(path, readFileSync(path, 'utf8').repeat(2))
  assert.throws(() => syncRuntimeVersions(valid), /exactly one/)
})

test('Wails installation verifies the pinned CLI and any linked Echo dependency', t => {
  const workflow = readFileSync(new URL('../.github/workflows/release-desktop.yml', import.meta.url), 'utf8')
  const step = workflow.split('      - name: Install Wails CLI\n')[1]
  const block = step?.match(/        run: \|\n((?:          .*\n|\n)+)/)?.[1]
  assert.ok(block, 'Wails installation step is missing')
  const program = block.replace(/^          /gm, '')
  const wailsVersion = 'v2.16.0'
  const echoVersion = 'v4.99.1'
  const buildInfo = `wails: go1.27.3\n\tmod\tgithub.com/wailsapp/wails/v2\t${wailsVersion}\th1:wails\n`
  const withEcho = buildInfo + `\tdep\tgithub.com/labstack/echo/v4\t${echoVersion}\th1:echo\n`
  for (const [name, overrides, expectedAttempts, success] of [
    ['first attempt', {}, 1, true],
    ['retry', { MOCK_FAILURES: '1' }, 2, true],
    ['exhausted retries', { MOCK_FAILURES: '9' }, 3, false],
    ['unpinned Wails', { MOCK_WAILS_VERSION: 'latest' }, 0, false],
    ['linked Echo matches', { MOCK_BUILD_INFO: withEcho }, 1, true],
    ['old Echo', { MOCK_BUILD_INFO: withEcho.replace(echoVersion, 'v4.1.0') }, 1, false],
    ['old Wails', { MOCK_BUILD_INFO: buildInfo.replace(wailsVersion, 'v2.1.0') }, 1, false],
    ['duplicate Echo', { MOCK_BUILD_INFO: withEcho + withEcho.split('\n').find(line => line.includes('labstack/echo')) + '\n' }, 1, false],
    ['replacement', { MOCK_BUILD_INFO: buildInfo + '\t=>\t../custom-echo\t(devel)\n' }, 1, false],
    ['replacement in a large inventory', { MOCK_BUILD_INFO: buildInfo + '\t=>\t../custom-echo\t(devel)\n' + '\tbuild\tlarge=value\n'.repeat(4000) }, 1, false],
    ['inspection failed', { MOCK_INSPECTION_FAILURE: '1' }, 1, false],
    ['Windows executable', { MOCK_GOEXE: '.exe' }, 1, true],
  ]) {
    const directory = mkdtempSync(join(tmpdir(), 'omni-wails-install-'))
    t.after(() => rmSync(directory, { recursive: true, force: true }))
    mkdirSync(join(directory, 'bin'))
    writeFileSync(join(directory, 'bin/go'), `#!${process.execPath}
const fs = require('node:fs');
const args = process.argv.slice(2);
if (args[0] === 'list') {
  const version = args.at(-1) === 'github.com/wailsapp/wails/v2' ? process.env.MOCK_WAILS_VERSION : process.env.MOCK_ECHO_VERSION;
  console.log(version); process.exit(0);
}
if (args[0] === 'env') {
  console.log(args[1] === 'GOEXE' ? process.env.MOCK_GOEXE : process.cwd()); process.exit(0);
}
if (args[0] === 'version' && args[1] === '-m') {
  if (args[2] !== process.cwd() + '/bin/wails' + process.env.MOCK_GOEXE) process.exit(2);
  process.stdout.write(process.env.MOCK_BUILD_INFO); process.exit(Number(process.env.MOCK_INSPECTION_FAILURE));
}
if (JSON.stringify(args) !== JSON.stringify(['install', 'github.com/wailsapp/wails/v2/cmd/wails@' + process.env.MOCK_WAILS_VERSION])) process.exit(2);
const attempts = fs.existsSync('attempts') ? Number(fs.readFileSync('attempts', 'utf8')) : 0;
fs.writeFileSync('attempts', String(attempts + 1));
process.exit(attempts < Number(process.env.MOCK_FAILURES) ? 1 : 0);
`, { mode: 0o755 })
    writeFileSync(join(directory, 'bin/sleep'), `#!${process.execPath}\nprocess.exit(0);\n`, { mode: 0o755 })
    const child = spawnSync('bash', ['-e', '-o', 'pipefail', '-c', program], {
      cwd: directory, encoding: 'utf8', timeout: 10000,
      env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}`,
        MOCK_WAILS_VERSION: wailsVersion, MOCK_ECHO_VERSION: echoVersion,
        MOCK_FAILURES: '0', MOCK_BUILD_INFO: buildInfo, MOCK_GOEXE: '', MOCK_INSPECTION_FAILURE: '0', ...overrides },
    })
    assert.equal(child.status === 0, success, `${name}: ${child.stderr}`)
    const attempts = join(directory, 'attempts')
    assert.equal(existsSync(attempts) ? Number(readFileSync(attempts, 'utf8')) : 0, expectedAttempts, name)
  }
})
