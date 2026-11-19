import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const source = resolve(process.argv[2] || '.oss-cache/picomatch-upstream')
const catalog = JSON.parse(readFileSync(join(root, 'scripts/security-patches/picomatch-upstream-tests.json'), 'utf8'))
const directory = mkdtempSync(join(process.env.RUNNER_TEMP || tmpdir(), 'omni-picomatch-compat-'))
try {
  cpSync(join(root, 'frontend/vendor/picomatch'), directory, { recursive: true })
  for (const file of catalog.files) {
    assert.match(file.path, /^test\/[a-zA-Z0-9./_-]+\.js$/)
    assert.ok(!file.path.split('/').includes('..'))
    const data = readFileSync(join(source, file.path))
    const blob = createHash('sha1').update(`blob ${data.length}\0`).update(data).digest('hex')
    assert.equal(blob, file.sha, 'Upstream test changed: ' + file.path)
    const path = join(directory, file.path)
    mkdirSync(dirname(path), { recursive: true }); writeFileSync(path, data)
  }
  const files = catalog.files.filter(x => !x.path.includes('/support/')).map(x => join(directory, x.path))
  const baseline = spawnSync(process.execPath, ['--input-type=commonjs', '-e',
    `const pm = require(${JSON.stringify(join(source, 'index.js'))}); process.stdout.write('original module loaded\\n'); pm.isMatch('a'.repeat(512) + '!', '+(*a)');`],
    { encoding: 'utf8', timeout: 3000, killSignal: 'SIGKILL' })
  assert.match(baseline.stdout || '', /original module loaded/)
  assert.equal(baseline.error?.code, 'ETIMEDOUT', 'The original version must reproduce the vulnerable input')
  console.log('Original 4.0.7 reproduced the ReDoS; the child was terminated at its deadline')
  const result = spawnSync(process.execPath, ['--require', join(root, 'scripts/picomatch-upstream-test-shim.cjs'),
    '--test', '--test-concurrency=1', '--test-reporter=spec', ...files], { encoding: 'utf8', timeout: 90_000,
    maxBuffer: 16 * 1024 * 1024, env: { ...process.env, NODE_PATH: join(root, 'frontend/node_modules') } })
  const output = (result.stdout || '') + (result.stderr || '')
  mkdirSync(join(root, '.oss-cache'), { recursive: true })
  writeFileSync(join(root, '.oss-cache/picomatch-compat-results.txt'), output)
  console.log(output.slice(result.status === 0 ? -1000 : -12000))
  assert.equal(result.status, 0, result.error?.message || 'Official picomatch compatibility assertions failed')
} finally { rmSync(directory, { recursive: true, force: true }) }
