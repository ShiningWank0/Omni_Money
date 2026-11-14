import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { verifyEvidence } from './wails-dev-integration.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const output = join(root, '.oss-cache/wails-dev-results')

export function verifyCLI(info, expected) {
  assert.doesNotMatch(info, /^\s*=>/m, 'Replaced CLI modules are forbidden')
  for (const path of ['github.com/wailsapp/wails/v2', 'github.com/labstack/echo/v4']) {
    const selected = [...info.matchAll(/^\s*(?:mod|dep)\s+(\S+)\s+(\S+)/gm)].filter(match => match[1] === path)
    if (path.endsWith('echo/v4') && !selected.length) continue
    assert.equal(selected.length, 1, `Missing or duplicate CLI module: ${path}`)
    assert.equal(selected[0][2], expected[path], `CLI module differs from project: ${path}`)
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv[2] === 'cli') {
      const go = args => {
        const result = spawnSync('go', args, { cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 * 1024 * 1024 })
        assert.equal(result.status, 0, result.stderr || String(result.error || 'Go inspection failed'))
        return result.stdout
      }
      const expected = Object.fromEntries(go(['list', '-mod=readonly', '-m', '-f', '{{.Path}} {{.Version}}{{if .Replace}} replaced{{end}}',
        'github.com/wailsapp/wails/v2', 'github.com/labstack/echo/v4']).trim().split('\n').map(line => {
        const fields = line.trim().split(/\s+/)
        assert.equal(fields.length, 2)
        return fields
      }))
      const info = go(['version', '-m', process.argv[3]])
      verifyCLI(info, expected)
      mkdirSync(output, { recursive: true })
      writeFileSync(join(output, 'cli-tool-build-info.txt'), info)
    } else {
      assert.equal(process.argv[2], 'result')
      const result = JSON.parse(readFileSync(join(output, 'result.json'), 'utf8'))
      const errors = JSON.parse(readFileSync(join(output, 'browser-errors.json'), 'utf8'))
      const selected = JSON.parse(readFileSync(join(output, 'selected-build.json'), 'utf8'))
      assert.deepEqual(result.modules, selected.modules)
      assert.equal(result.binarySHA256, selected.binarySHA256)
      verifyEvidence(result, errors, process.argv[3], process.env.GITHUB_SHA)
      if (process.argv[3] === 'vite') {
        assert.match(result.cliBinarySHA256, /^[a-f0-9]{64}$/)
        for (const file of ['cli-build-info.txt', 'cli-tool-build-info.txt']) assert.ok(readFileSync(join(output, file), 'utf8').length)
      }
    }
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
