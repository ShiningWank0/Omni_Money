import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { lstatSync, readFileSync, realpathSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { verifyDevBuild } from './wails-dev-integration.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

export function inspectDevBuild(argument, platform = 'linux/amd64', directory = root) {
  assert.ok(argument && lstatSync(argument).isFile() && !lstatSync(argument).isSymbolicLink(), 'Pass a regular development binary')
  const binary = realpathSync(argument)
  const go = args => {
    const result = spawnSync('go', args, { cwd: directory, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
    assert.equal(result.status, 0, result.stderr || String(result.error || 'Go inspection failed'))
    return result.stdout
  }
  const modules = Object.fromEntries(go(['list', '-mod=readonly', '-m', '-f', '{{.Path}} {{.Version}}{{if .Replace}} replaced{{end}}',
    'github.com/wailsapp/wails/v2', 'github.com/labstack/echo/v4']).trim().split('\n').map(line => {
    const fields = line.trim().split(/\s+/)
    assert.equal(fields.length, 2, 'Unpinned or replaced selected module')
    return fields
  }))
  const info = go(['version', '-m', binary])
  verifyDevBuild(info, modules, platform)
  return { platform, modules, binarySHA256: createHash('sha256').update(readFileSync(binary)).digest('hex') }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { console.log(JSON.stringify(inspectDevBuild(process.argv[2], process.argv[3]))) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
